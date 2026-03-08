package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pccr10001/smsie/internal/api"
	"github.com/pccr10001/smsie/internal/calling"
	"github.com/pccr10001/smsie/internal/config"
	"github.com/pccr10001/smsie/internal/mccmnc"
	"github.com/pccr10001/smsie/internal/model"
	"github.com/pccr10001/smsie/internal/worker"
	"github.com/pccr10001/smsie/pkg/logger"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	// 1. Load Config
	config.LoadConfig()

	// 2. Init Logger
	logger.InitLogger(config.AppConfig.Log.Level)
	logger.Log.Info("Starting SMS Dashboard...")

	// Load MCCMNC
	if err := mccmnc.LoadOperators("mcc_mnc.json"); err != nil {
		logger.Log.Warnf("Failed to load MCC/MNC data: %v", err)
	}

	// 3. Init Database
	db := initDB()

	// 4. Init Router
	if config.AppConfig.Server.Mode == "release" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.Default()

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"message": "pong",
		})
	})

	// 5. Start Worker Manager
	wm := worker.NewManager(db)
	wm.Start()
	defer wm.Stop()

	stdLogger := log.New(os.Stdout, "[calling] ", log.LstdFlags|log.Lmicroseconds)
	callMgr, err := calling.NewManager(calling.Config{
		STUNServers: config.AppConfig.Calling.STUNServers,
		UDPPortMin:  config.AppConfig.Calling.UDPPortMin,
		UDPPortMax:  config.AppConfig.Calling.UDPPortMax,
		Audio: calling.AudioConfig{
			DeviceKeyword:    config.AppConfig.Calling.Audio.DeviceKeyword,
			OutputDeviceName: config.AppConfig.Calling.Audio.OutputDeviceName,
			SampleRate:       config.AppConfig.Calling.Audio.SampleRate,
			Channels:         config.AppConfig.Calling.Audio.Channels,
			BitsPerSample:    config.AppConfig.Calling.Audio.BitsPerSample,
			CaptureChunkMs:   config.AppConfig.Calling.Audio.CaptureChunkMs,
			PlaybackChunkMs:  config.AppConfig.Calling.Audio.PlaybackChunkMs,
		},
		SIP: calling.SIPConfig{
			RegisterExpires:    config.AppConfig.Calling.SIP.RegisterExpires,
			LocalHost:          config.AppConfig.Calling.SIP.LocalHost,
			LocalPort:          config.AppConfig.Calling.SIP.LocalPort,
			RTPBindIP:          config.AppConfig.Calling.SIP.RTPBindIP,
			RTPPortMin:         config.AppConfig.Calling.SIP.RTPPortMin,
			RTPPortMax:         config.AppConfig.Calling.SIP.RTPPortMax,
			InviteTimeoutSec:   config.AppConfig.Calling.SIP.InviteTimeoutSec,
			DTMFMethod:         config.AppConfig.Calling.SIP.DTMFMethod,
			DTMFDurationMillis: config.AppConfig.Calling.SIP.DTMFDurationMillis,
		},
	}, stdLogger)
	if err != nil {
		logger.Log.Fatalf("Failed to init calling manager: %v", err)
	}
	defer callMgr.CloseAll()

	sipSyncStop := make(chan struct{})
	defer close(sipSyncStop)
	go runSIPInboundSyncLoop(db, wm, callMgr, calling.SIPConfig{
		LocalHost:          config.AppConfig.Calling.SIP.LocalHost,
		LocalPort:          config.AppConfig.Calling.SIP.LocalPort,
		RTPBindIP:          config.AppConfig.Calling.SIP.RTPBindIP,
		RTPPortMin:         config.AppConfig.Calling.SIP.RTPPortMin,
		RTPPortMax:         config.AppConfig.Calling.SIP.RTPPortMax,
		InviteTimeoutSec:   config.AppConfig.Calling.SIP.InviteTimeoutSec,
		DTMFMethod:         config.AppConfig.Calling.SIP.DTMFMethod,
		DTMFDurationMillis: config.AppConfig.Calling.SIP.DTMFDurationMillis,
	}, stdLogger, sipSyncStop)

	// 6. Start Server
	// Load Templates
	r.LoadHTMLGlob("web/templates/*")
	r.Static("/static", "./web/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// Setup Routes
	mh := api.NewModemHandler(db, wm, callMgr)
	sh := api.NewSMSHandler(db)
	wh := api.NewWebhookHandler(db)
	uh := api.NewUserHandler(db)
	akh := api.NewAPIKeyHandler(db)

	apiGroup := r.Group("/api/v1")
	{
		apiGroup.POST("/login", uh.Login)

		// Authenticated Routes
		authGroup := apiGroup.Group("/")
		authGroup.Use(api.AuthMiddleware(db))
		authGroup.Use(api.APIKeyAllowedOnly())
		{
			authGroup.POST("/change_password", uh.ChangePassword)
			authGroup.GET("/apikeys", akh.ListMyAPIKeys)
			authGroup.POST("/apikeys", akh.CreateMyAPIKey)
			authGroup.POST("/apikeys/:id/rotate", akh.RotateMyAPIKey)
			authGroup.DELETE("/apikeys/:id", akh.DeleteMyAPIKey)

			authGroup.GET("/modems", mh.ListModems)
			authGroup.GET("/modems/:iccid", mh.GetModem)
			authGroup.PUT("/modems/:iccid", mh.UpdateModem)
			authGroup.POST("/modems/:iccid/scan", mh.ScanNetworks)
			authGroup.POST("/modems/:iccid/operator", mh.SetOperator)
			authGroup.POST("/modems/:iccid/at", mh.ExecuteAT)
			authGroup.POST("/modems/:iccid/input", mh.ExecuteInput)
			authGroup.GET("/modems/:iccid/call/state", mh.GetCallState)
			authGroup.POST("/modems/:iccid/call/dial", mh.Dial)
			authGroup.POST("/modems/:iccid/call/hangup", mh.Hangup)
			authGroup.POST("/modems/:iccid/call/dtmf", mh.DTMF)
			authGroup.POST("/modems/:iccid/reboot", mh.Reboot)
			authGroup.POST("/modems/:iccid/send", mh.SendSMS)
			authGroup.GET("/sms", sh.ListSMS)
			authGroup.GET("/modems/:iccid/ws", mh.WS)

			// Admin Only
			adminGroup := authGroup.Group("/")
			adminGroup.Use(api.AdminOnly())
			{
				adminGroup.GET("/webhooks", wh.ListWebhooks)
				adminGroup.POST("/webhooks", wh.CreateWebhook)
				adminGroup.DELETE("/webhooks/:id", wh.DeleteWebhook)
				adminGroup.DELETE("/modems/:iccid", mh.DeleteModem)

				adminGroup.GET("/users", uh.ListUsers)
				adminGroup.POST("/users", uh.CreateUser)
				adminGroup.GET("/users/:id/permissions", uh.ListUserPermissions)
				adminGroup.PUT("/users/:id/permissions", uh.UpdateUserPermissions)
				adminGroup.DELETE("/users/:id", uh.DeleteUser)
			}
		}
	}

	port := config.AppConfig.Server.Port
	logger.Log.Infof("Server listening on %s", port)
	if err := r.Run(port); err != nil {
		logger.Log.Fatalf("Server failed to start: %v", err)
	}
}

func initDB() *gorm.DB {
	var db *gorm.DB
	var err error

	driver := config.AppConfig.Database.Driver
	dsn := config.AppConfig.Database.DSN

	switch driver {
	case "mysql":
		db, err = gorm.Open(mysql.Open(dsn), &gorm.Config{})
	default:
		// Default to SQLite (pure Go)
		if dsn == "" {
			dsn = "smsie_v2.db"
		}
		db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	}

	if err != nil {
		logger.Log.Fatalf("Failed to connect database (%s): %v", driver, err)
	}

	// Auto Migrate
	if err := autoMigrateSchema(db); err != nil {
		logger.Log.Fatalf("Failed to migrate database schema: %v", err)
	}

	// Init Admin
	var count int64
	db.Model(&model.User{}).Count(&count)
	if count == 0 {
		// Generate random password
		const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		ret := make([]byte, 12)
		for i := 0; i < 12; i++ {
			num, err := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
			if err != nil {
				logger.Log.Fatalf("Failed to generate random password: %v", err)
			}
			ret[i] = chars[num.Int64()]
		}
		randPw := string(ret)

		// Hash it using bcrypt
		bytes, err := bcrypt.GenerateFromPassword([]byte(randPw), 14)
		if err != nil {
			logger.Log.Fatalf("Failed to hash password: %v", err)
		}
		hash := string(bytes)

		admin := model.User{
			Username:     "admin",
			PasswordHash: hash,
			Role:         "admin",
		}
		db.Create(&admin)
		logger.Log.Warnf("INITIAL ADMIN CREATED. Username: admin, Password: %s", randPw)
	}

	return db
}

func autoMigrateSchema(db *gorm.DB) error {
	if err := migrateLegacyModemSIPColumns(db); err != nil {
		return err
	}
	return db.AutoMigrate(&model.User{}, &model.Modem{}, &model.SMS{}, &model.Webhook{}, &model.UserModemPermission{}, &model.APIKey{})
}

func migrateLegacyModemSIPColumns(db *gorm.DB) error {
	migrator := db.Migrator()
	if !migrator.HasTable(&model.Modem{}) {
		return nil
	}

	legacyColumns := map[string]string{
		"s_ip_enabled":         "sip_enabled",
		"s_ip_username":        "sip_username",
		"s_ip_password":        "sip_password",
		"s_ip_proxy":           "sip_proxy",
		"s_ip_port":            "sip_port",
		"s_ip_domain":          "sip_domain",
		"s_ip_transport":       "sip_transport",
		"s_ip_register":        "sip_register",
		"s_ip_tls_skip_verify": "sip_tls_skip_verify",
		"s_ip_listen_port":     "sip_listen_port",
	}

	for legacy, current := range legacyColumns {
		legacyExists := migrator.HasColumn(&model.Modem{}, legacy)
		currentExists := migrator.HasColumn(&model.Modem{}, current)
		switch {
		case legacyExists && !currentExists:
			if err := migrator.RenameColumn(&model.Modem{}, legacy, current); err != nil {
				return fmt.Errorf("rename modems.%s to %s: %w", legacy, current, err)
			}
		case legacyExists && currentExists:
			logger.Log.Warnf("legacy modem column %s still exists alongside %s; leaving both columns in place", legacy, current)
		}
	}
	return nil
}

func runSIPInboundSyncLoop(db *gorm.DB, wm *worker.Manager, callMgr *calling.Manager, baseCfg calling.SIPConfig, stdLogger *log.Logger, stop <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	syncOnce := func() {
		if err := syncSIPInboundLines(db, wm, callMgr, baseCfg); err != nil {
			stdLogger.Printf("sip inbound sync failed: %v", err)
		}
	}

	syncOnce()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			syncOnce()
		}
	}
}

func syncSIPInboundLines(db *gorm.DB, wm *worker.Manager, callMgr *calling.Manager, baseCfg calling.SIPConfig) error {
	var modems []model.Modem
	if err := db.Find(&modems).Error; err != nil {
		return err
	}

	reservedPorts := map[int]string{}
	activeLines := map[string]struct{}{}

	for _, modem := range modems {
		lineID := sipLineID(modem.ICCID)
		workerForModem := wm.GetWorkerByICCID(modem.ICCID)
		if workerForModem == nil || !workerForModem.IsUACReady() || !modem.SIPEnabled {
			continue
		}

		cfg, ok := buildModemSIPConfig(modem, baseCfg)
		if !ok {
			continue
		}

		listenPort, err := ensureSIPListenerPort(db, modem.ICCID, cfg.LocalPort, cfg.Transport, reservedPorts)
		if err != nil {
			return fmt.Errorf("assign listener port for %s: %w", modem.ICCID, err)
		}
		cfg.LocalPort = listenPort

		iccid := modem.ICCID
		err = callMgr.SyncSIPInbound(lineID, cfg, calling.SIPInboundHooks{
			ICCID: iccid,
			ResolveModem: func() (string, calling.ModemTarget, error) {
				w := wm.GetWorkerByICCID(iccid)
				if w == nil {
					return "", calling.ModemTarget{}, fmt.Errorf("modem %s not active", iccid)
				}
				if !w.IsUACReady() {
					return "", calling.ModemTarget{}, fmt.Errorf("modem %s uac not ready", iccid)
				}
				vid, pid := w.UACIdentity()
				return iccid, calling.ModemTarget{PortName: w.PortName, VID: vid, PID: pid}, nil
			},
			DialModem: func(usedICCID, number string) error {
				w := wm.GetWorkerByICCID(usedICCID)
				if w == nil {
					return fmt.Errorf("modem %s not active", usedICCID)
				}
				return w.Dial(number)
			},
			HangupModem: func(usedICCID string) error {
				w := wm.GetWorkerByICCID(usedICCID)
				if w == nil {
					return nil
				}
				return w.Hangup()
			},
			SendDTMF: func(usedICCID, tone string) error {
				w := wm.GetWorkerByICCID(usedICCID)
				if w == nil {
					return fmt.Errorf("modem %s not active", usedICCID)
				}
				_, err := w.ExecuteAT(`AT+VTS="`+tone+`"`, 5*time.Second)
				return err
			},
		})
		if err != nil {
			return fmt.Errorf("sync line %s: %w", lineID, err)
		}

		activeLines[lineID] = struct{}{}
	}

	return callMgr.PruneSIPInboundLines(activeLines)
}

func buildModemSIPConfig(modem model.Modem, base calling.SIPConfig) (calling.SIPConfig, bool) {
	username := strings.TrimSpace(modem.SIPUsername)
	proxy := strings.TrimSpace(modem.SIPProxy)
	if username == "" || proxy == "" {
		return calling.SIPConfig{}, false
	}

	transport := strings.ToLower(strings.TrimSpace(modem.SIPTransport))
	if transport == "" {
		transport = "udp"
	}
	port := modem.SIPPort
	if port <= 0 {
		if transport == "tls" {
			port = 5061
		} else {
			port = 5060
		}
	}

	cfg := base
	cfg.Enabled = true
	cfg.Username = username
	cfg.Password = modem.SIPPassword
	cfg.Proxy = proxy
	cfg.Port = port
	cfg.Domain = strings.TrimSpace(modem.SIPDomain)
	cfg.Transport = transport
	cfg.TLSSkipVerify = modem.SIPTLSSkipVerify
	cfg.Register = modem.SIPRegister
	return cfg, true
}

func sipLineID(iccid string) string {
	return fmt.Sprintf("sip-line-%s", strings.TrimSpace(iccid))
}

func ensureSIPListenerPort(db *gorm.DB, iccid string, basePort int, transport string, reserved map[int]string) (int, error) {
	if basePort <= 0 {
		basePort = 5060
	}

	var modem model.Modem
	err := db.First(&modem, "iccid = ?", iccid).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, err
		}
		modem = model.Modem{ICCID: iccid}
		if err := db.Create(&modem).Error; err != nil {
			return 0, err
		}
	}

	if modem.SIPListenPort > 0 {
		if other, ok := reserved[modem.SIPListenPort]; ok && other != iccid {
			return 0, fmt.Errorf("fixed sip listen port %d already reserved by %s", modem.SIPListenPort, other)
		}
		reserved[modem.SIPListenPort] = iccid
		return modem.SIPListenPort, nil
	}

	for port := basePort; port <= 65535; port++ {
		if other, ok := reserved[port]; ok && other != iccid {
			continue
		}
		available, err := isSIPListenPortAvailable(transport, port)
		if err != nil {
			return 0, err
		}
		if !available {
			continue
		}
		if err := db.Model(&model.Modem{}).Where("iccid = ?", iccid).Update("sip_listen_port", port).Error; err != nil {
			return 0, err
		}
		reserved[port] = iccid
		return port, nil
	}

	return 0, fmt.Errorf("no free sip listen port available from %d", basePort)
}

func isSIPListenPortAvailable(transport string, port int) (bool, error) {
	addr := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "udp":
		conn, err := net.ListenPacket("udp", addr)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "only one usage") || strings.Contains(strings.ToLower(err.Error()), "address already in use") {
				return false, nil
			}
			return false, err
		}
		_ = conn.Close()
		return true, nil
	default:
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "only one usage") || strings.Contains(strings.ToLower(err.Error()), "address already in use") {
				return false, nil
			}
			return false, err
		}
		_ = ln.Close()
		return true, nil
	}
}
