package main

import (
	"crypto/rand"
	"math/big"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pccr10001/smsie/internal/api"
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
	// ... (content omitted for brevity, ensure import matches)
	// (Wait, replacement content needs to be exact match for block or line. I'll replace import block and connection line separately or together if contiguous)

	// Better approach: Replace imports first.
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

	// 6. Start Server
	// Load Templates
	r.LoadHTMLGlob("web/templates/*")
	r.Static("/static", "./web/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// Setup Routes
	mh := api.NewModemHandler(db, wm)
	sh := api.NewSMSHandler(db)
	wh := api.NewWebhookHandler(db)
	uh := api.NewUserHandler(db)

	apiGroup := r.Group("/api/v1")
	{
		apiGroup.POST("/login", uh.Login)

		// Authenticated Routes
		authGroup := apiGroup.Group("/")
		authGroup.Use(api.AuthMiddleware(db))
		{
			authGroup.POST("/change_password", uh.ChangePassword)

			authGroup.GET("/modems", mh.ListModems)
			authGroup.GET("/modems/:iccid", mh.GetModem)
			authGroup.PUT("/modems/:iccid", mh.UpdateModem)
			authGroup.POST("/modems/:iccid/scan", mh.ScanNetworks)
			authGroup.POST("/modems/:iccid/operator", mh.SetOperator)
			authGroup.POST("/modems/:iccid/at", mh.ExecuteAT)
			authGroup.POST("/modems/:iccid/input", mh.ExecuteInput)
			authGroup.GET("/sms", sh.ListSMS)

			// Admin Only
			adminGroup := authGroup.Group("/")
			adminGroup.Use(api.AdminOnly())
			{
				adminGroup.GET("/webhooks", wh.ListWebhooks)
				adminGroup.POST("/webhooks", wh.CreateWebhook)
				adminGroup.DELETE("/webhooks/:id", wh.DeleteWebhook)

				adminGroup.GET("/users", uh.ListUsers)
				adminGroup.POST("/users", uh.CreateUser)
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
	db.AutoMigrate(&model.User{}, &model.Modem{}, &model.SMS{}, &model.Webhook{})

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
