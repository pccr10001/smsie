# smsie

**smsie** is a robust SMS management dashboard written in Go. It allows you to manage multiple GSM/LTE modems receive SMS messages, and integrate with external services via webhooks.

## 🚀 Features

- **Modem Management**: Automatically scans and detects serial modems. Tracks signal strength, operator name, and registration status in real-time (runtime state, not persisted as DB source-of-truth).
- **SMS Operations**:
  - **Read**: View received SMS messages with pagination and search.`
  - **Send**: Send SMS with PDU supported.
  - **Immediate Scan**: Instant SMS detection upon receiving `+CMTI` notifications.
- **AT Command Terminal**: Execute raw AT commands directly on modems for debugging and advanced configuration.
- **Voice Call (Dial/Hangup)**: Basic browser call controls per modem with call state tracking (`idle`, `dialing`, `in_call`).
  - Dial UI appears only when modem `AT+QCFG="usbcfg"` probe indicates UAC enabled, so we only support Quectel modules currently.
  - Browser `Call` always uses microphone + WebRTC signaling first, then bridges to modem UAC audio before `ATD` is sent.
  - PortAudio modem audio bridge initializes only when dial is requested.
  - Automatically UAC and USB device mapping.
- **Per-Modem SIP Client / FXO External Line**:
  - Each UAC-ready modem can enable its own SIP client from modem settings.
  - SIP registration/listener state is runtime-managed and shown per ICCID.
  - Multiple UAC-ready modems can run multiple SIP connections at the same time.
- **Webhooks**: Forward received SMS messages to **Telegram** and **Slack** automatically.
- **User Management**:
  - Role-based access control (Admin/User).
  - Secure password storage using **Bcrypt**.
  - Modem access restrictions per user.
- **Database Support**: Supports both **SQLite** (default) and **MySQL** for flexible deployment.
- **Modern UI**: Responsive web interface built with Bootstrap and jQuery.
- **Cross Platform**: Windows / Linux are supported.

## 🛠 Prerequisites

- **Go 1.20+**
- **Serial Drivers**: Ensure drivers for your modems are installed.
  - Following modems are tested:
    - Quectel EC20
    - Quectel EC800M
    - OpenLuat Air780E
  - Following modems with voice supported are tested:
    - Quectel EC20

## 📦 Installation

1.  **Clone the repository:**

    ```bash
    git clone https://github.com/pccr10001/smsie.git
    cd smsie
    ```

2.  **Download Dependencies:**

    ```bash
    go mod tidy
    ```

### Linux 

3.  **Build the application:**
    ```bash
    sudo apt install portaudio19-dev libusb-1.0-0-dev ffmpeg
    go build # -tags nouac to disable UAC
    ```

### Windows
3. **Build the application:**
    ```bash
   # Use mingw64 to build.
   pacman -S  mingw-w64-x86_64-ffmpeg  mingw-w64-x86_64-portaudio  mingw-w64-x86_64-libusb
    go build # -tags nouac to disable UAC
    ```

## ⚙️ Configuration

The application uses a `config.yaml` file. If not present, create one based on the example structure:

```yaml
server:
  port: ":8080" # Web server port
  mode: "release" # "debug" or "release"

database:
  driver: "sqlite" # "sqlite" or "mysql"
  dsn: "smsie.db" # Filename for SQLite, or DSN string for MySQL
  # dsn: "user:pass@tcp(127.0.0.1:3306)/smsie?charset=utf8mb4&parseTime=True&loc=Local"

serial:
  scan_interval: "5s" # How often to check for port changes
  exclude_ports: ["COM1"] # Serial ports to ignore ["/dev/ttyUSB0"]
  init_at_commands: # Commands to run on modem detection
    - "ATE0" # Echo off
    - "AT+CMEE=1" # Verbose errors
    - "AT+COPS=3,2" # Numberic operator name

calling:
  stun_servers:
    - "stun:stun.l.google.com:19302"
  udp_port_min: 40000
  udp_port_max: 40100
  sip:
    register_expires: 300
    local_host: ""
    local_port: 5060
    rtp_bind_ip: "0.0.0.0"
    rtp_port_min: 30000
    rtp_port_max: 30010
    invite_timeout_sec: 30
    dtmf_method: "info"
    dtmf_duration_ms: 160
  audio:
    # Optional fallback keyword for PortAudio device matching.
    # Usually no manual config is needed for Quectel UAC flow.
    device_keyword: "AC Interface"
    output_device_name: ""
    sample_rate: 8000
    channels: 1
    bits_per_sample: 16
    capture_chunk_ms: 40
    playback_chunk_ms: 100

log:
  level: "info" # debug, info, warn, error
```

Notes:
- SIP account settings are not stored in global `config.yaml`.
- Each modem has its own SIP settings in the modem settings dialog: `enable`, `username`, `password`, `proxy`, `port`, `domain`, `transport`, `register`, `tls skip verify`, and optional fixed `listener port`.
- Global `calling.sip` config only defines shared SIP runtime defaults such as listener port base, RTP range, REGISTER expiry, invite timeout, and DTMF behavior.
- If a modem is not present or UAC is not ready, smsie automatically stops that modem's SIP client and hides browser call controls.
- Existing databases are migrated on startup; older legacy modem SIP columns named `s_ip_*` are renamed to `sip_*` automatically.

## Voice Calling (Quectel UAC)

- On modem probe, smsie sends `AT+QCFG="usbcfg"` to check UAC support.
- smsie checks the trailing 7 flags of `+QCFG: "USBCFG",...` and requires the last flag to be `1` (UAC enabled).
- Only when UAC is ready will the browser call UI appear in frontend.
- Browser `Call` is always a WebRTC-originated call:
  - Browser microphone + WebRTC signaling must complete first.
  - Then backend initializes the PortAudio <-> modem UAC bridge.
  - Finally `ATD<number>;` is sent to the modem.
- If dial fails, backend closes the WebRTC session and audio bridge automatically.
- USB/UAC matching is automatic:
  - Uses modem port (`COMx`/`ttyUSBx`) to resolve USB identity.
  - Uses QCFG-derived VID/PID and USB enumeration (`gousb`) to locate target UAC device.

## SIP Client / FXO External Line

- SIP client is configured per modem, not globally.
- Each modem settings page can enable SIP client and set:
  - `username`, `password`, `proxy`, `port`, optional `domain`
  - `transport`: `udp`, `tcp`, or `tls`
  - `register`, `tls skip verify`, and optional fixed `listener port`
- SIP client starts only when that ICCID is present and UAC is ready.
- If the modem is unplugged or UAC is unavailable, smsie automatically stops that modem's SIP client and the frontend will not show SIP/WebRTC call options for that modem.
- One SIP listener/registration instance is maintained per modem, so multiple UAC-ready modems can keep multiple SIP connections at the same time.
- Listener port behavior:
  - If modem `listener port` is set, smsie keeps using that fixed port.
  - If it is `0`, smsie auto-assigns a free port starting from `calling.sip.local_port` and persists it to the modem profile.
- SIP INVITE handling for external line use:
  - Inbound SIP `INVITE <number>@<listener>` triggers modem dialing with `ATD<number>;`.
  - Audio is bridged through the modem's UAC device, not through browser WebRTC.
- TLS notes:
  - Outbound SIP client certificate verification can be disabled with per-modem `tls skip verify`.
  - SIP TLS listener uses a runtime-generated self-signed certificate unless you extend deployment with your own certificate handling.
- RTP local port range is controlled by `calling.sip.rtp_port_min` / `calling.sip.rtp_port_max`.
- DTMF keypad and API DTMF requests are converted to in-call SIP DTMF (`INFO` with `application/dtmf-relay`) for SIP calls.
- Browser frontend does not originate SIP calls directly. Browser `Call` always uses WebRTC/UAC. SIP origination is for backend/API integrations or VoIP server initiated calls to the modem listener.

## Enable UAC on modem
- Send `AT+QCFG="usbcfg",0x2C7C,0x0125,1,1,1,1,1,1,1` in AT command terminal.

## Enable VoLTE on modem
```
# Enable IMS
AT+QCFG="ims",1

OK

# Check MBN config in modem, `2,1,1` is mean `OpenMkt-Commercial-CT` is selected and activated
AT+QMBNCFG="List"

+QMBNCFG: "List",0,0,0,"ROW_Generic_3GPP",0x05010824,201806201
+QMBNCFG: "List",1,0,0,"OpenMkt-Commercial-CU",0x05011510,201911151
+QMBNCFG: "List",2,1,1,"OpenMkt-Commercial-CT",0x0501131C,201911141
+QMBNCFG: "List",3,0,0,"Volte_OpenMkt-Commercial-CMCC",0x05012011,201904261

OK

# We ned to select generic MBN profile to register with IMS
# Disable auto selecting MBN profile
AT+QMBNCFG="AutoSel",0

OK

# Deactivate current MBN profile
AT+QMBNCFG="deactivate"

OK

# Activate generic MBN profile
AT+QMBNCFG="select","ROW_Generic_3GPP"

OK

# Reboot modem
AT+CFUN=1,1

# Check MBN profiles
AT+QMBNCFG="List"

+QMBNCFG: "List",0,1,1,"ROW_Generic_3GPP",0x05010824,201806201
+QMBNCFG: "List",1,0,0,"OpenMkt-Commercial-CU",0x05011510,201911151
+QMBNCFG: "List",2,0,0,"OpenMkt-Commercial-CT",0x0501131C,201911141
+QMBNCFG: "List",3,0,0,"Volte_OpenMkt-Commercial-CMCC",0x05012011,201904261

OK

# Check status of IMS
AT+QCFG="ims"

+QCFG: "ims",1,1    # `1,1` is mean IMS is activated

OK
```

## Linux Permissions (for calling/UAC)

For voice calling on Linux, the process user needs access to serial, USB bus, and audio devices:

- Add user groups (example):
  - `sudo usermod -aG dialout,audio,plugdev <your-user>`
- Ensure access to:
  - `/dev/ttyUSB*` (AT command port)
  - `/dev/bus/usb/*` (USB enumeration via `gousb`/libusb)
  - ALSA/PulseAudio/PipeWire devices used by PortAudio
- If needed, create udev rules for Quectel USB permissions (VID/PID from modem `AT+QCFG="usbcfg"`).

After group/udev changes, re-login or reboot.

## 📂 Data Files

### `mcc_mnc.json`

The application uses `mcc_mnc.json` to map numeric MCC/MNC codes to human-readable operator names. You can download a standard dataset or create one with the following structure:

```json
[
  {
    "type": "LTE",
    "country": "Taiwan",
    "country_code": "886",
    "mcc": "466",
    "mnc": "92",
    "name": "Chunghwa Telecom",
    "namel": "Chunghwa Telecom",
    "iso": "tw"
  },
  {
    "type": "LTE",
    "country": "Taiwan",
    "country_code": "886",
    "mcc": "466",
    "mnc": "01",
    "name": "Far EasTone",
    "namel": "Far EasTone",
    "iso": "tw"
  }
]
```

## 🚀 Usage

1.  **Run the server:**

    ```bash
    ./smsie
    ```

2.  **Access the Dashboard:**
    Open your browser and navigate to `http://localhost:8080`.

3.  **Initial Login:**
    - On the **first run** (if the database is empty), the server will create a default `admin` user.
    - **Check the console logs** for the randomly generated password:
      ```text
      WARN [...] INITIAL ADMIN CREATED. Username: admin, Password: <random-string>
      ```
    - Log in with these credentials and change your password immediately.

## 🔗 API Integration

smsie provides a RESTful API for integration.

- **Base URL**: `/api/v1`
- **Authentication**: Bearer Token (JWT)

### Key Endpoints:

- `GET /modems`: List connected modems with runtime worker/UAC/SIP state.
- `GET /modems/:iccid`: Get one modem including per-modem SIP settings/status.
- `PUT /modems/:iccid`: Update modem name and per-modem SIP settings.
- `DELETE /modems/:iccid`: Delete modem profile (admin only).
- `POST /modems/:iccid/at`: Execute AT command.
- `POST /modems/:iccid/input`: Send raw input (e.g., for `^Z`).
- `GET /modems/:iccid/call/state`: Get current call state, UAC readiness, and SIP listener/register state.
- `GET /modems/:iccid/ws`: Browser WebRTC signaling endpoint (WebSocket, token via query `?token=`).
- `POST /modems/:iccid/call/dial`: Dial a number. Browser UI uses body `{ "number": "09xxxxxxxx" }` after WebRTC signaling is ready.
- `POST /modems/:iccid/call/hangup`: Hang up current call. If body `via` is omitted, server auto-selects the active call leg.
- `POST /modems/:iccid/call/dtmf`: Send in-call DTMF. Body: `{ "tone": "5" }`. If body `via` is omitted, server auto-selects the active call leg.
- `GET /sms`: List SMS messages.

See the `openapi/` directory (if available) or code structure for detailed API definitions.

## 🚢 Deployment

### Systemd (Linux)

A `smsie.service` file is included for easier deployment on Linux systems using systemd.

1.  **Move the binary and assets** to `/opt/smsie` (or modify the service file paths).
2.  **Copy the service file:**
    ```bash
    sudo cp smsie.service /etc/systemd/system/
    ```
3.  **Reload Daemon & Enable Config:**
    ```bash
    sudo systemctl daemon-reload
    sudo systemctl enable smsie
    sudo systemctl start smsie
    ```
4.  **View Logs:**
    ```bash
    journalctl -u smsie -f
    ```

### Docker

A `Dockerfile` is provided for containerized deployment, or you can use the pre-built image from GHCR.

1.  **Run the Container:**

    ```bash
    # Run using GHCR image (Automatic Port Scanning enabled)
    docker run -d -p 8080:8080 --name smsie \
      --privileged \
      --device=/dev:/dev \
      -v smsie_data:/app/data \
      ghcr.io/pccr10001/smsie:latest
    ```

    > **Note:** To use a custom config, mount it with `-v $(pwd)/config.yaml:/app/config.yaml`.

### Docker Compose

A `docker-compose.yml` is provided using the GHCR image.

1.  **Download `docker-compose.yml`:**
    (Ensure you have the file from the repository)

2.  **Start the service:**

    ```bash
    docker-compose up -d
    ```

3.  **View Logs:**

    ```bash
    docker-compose logs -f
    ```

    ```bash
    docker-compose down
    ```

## 🤝 Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

## 📄 License

[GPL-3.0](https://choosealicense.com/licenses/gpl-3.0/)
