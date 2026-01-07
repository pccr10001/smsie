# smsie

**smsie** is a robust SMS management dashboard written in Go. It allows you to manage multiple GSM/LTE modems receive SMS messages, and integrate with external services via webhooks.

## 🚀 Features

- **Modem Management**: Automatically scans and detects serial modems. Tracks signal strength, operator name, and registration status in real-time.
- **SMS Operations**:
  - **Read**: View received SMS messages with pagination and search.`
  - **Immediate Scan**: Instant SMS detection upon receiving `+CMTI` notifications.
- **AT Command Terminal**: Execute raw AT commands directly on modems for debugging and advanced configuration.
- **Webhooks**: Forward received SMS messages to **Telegram** and **Slack** automatically.
- **User Management**:
  - Role-based access control (Admin/User).
  - Secure password storage using **Bcrypt**.
  - Modem access restrictions per user.
- **Database Support**: Supports both **SQLite** (default) and **MySQL** for flexible deployment.
- **Modern UI**: Responsive web interface built with Bootstrap and jQuery.

## 🛠 Prerequisites

- **Go 1.20+**
- **Serial Drivers**: Ensure drivers for your modems are installed.
  - Following modems are tested:
    - Quectel EC20
    - Quectel EC800M
    - OpenLuat Air780E

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

3.  **Build the application:**
    ```bash
    go build
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

log:
  level: "info" # debug, info, warn, error
```

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

- `GET /modems`: List connected modems.
- `POST /modems/:iccid/at`: Execute AT command.
- `POST /modems/:iccid/input`: Send raw input (e.g., for `^Z`).
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

A `Dockerfile` is provided for containerized deployment.

1.  **Build the Image:**

    ```bash
    docker build -t smsie .
    ```

2.  **Run the Container:**

    ```bash
    # Run with default config (sqlite in container)
    docker run -d -p 8080:8080 --name smsie \
      --device=/dev/ttyUSB0:/dev/ttyUSB0 \
      -v smsie_data:/app/data \
      smsie

    # Run with custom config
    docker run -d -p 8080:8080 --name smsie \
      --device=/dev/ttyUSB0:/dev/ttyUSB0 \
      -v $(pwd)/config.yaml:/app/config.yaml \
      -v smsie_data:/app/data \
      smsie
    ```

    > **Note on Serial Ports:** You must map the serial device (e.g., `--device=/dev/ttyUSB0:/dev/ttyUSB0`) for `smsie` to access the modem.

    **Automatic Port Scanning (Advanced)**
    To allow `smsie` to automatically discover and hot-plug new modems, you must run the container in privileged mode and map the entire `/dev` directory:

    ```bash
    docker run -d -p 8080:8080 --name smsie \
      --privileged \
      --device=/dev:/dev \
      -v smsie_data:/app/data \
      smsie
    ```

### Docker Compose

A `docker-compose.yml` is also provided. It is configured by default for **Automatic Port Scanning** (Privileged mode).

1.  **Start the service:**

    ```bash
    docker-compose up -d
    ```

2.  **View Logs:**

    ```bash
    docker-compose logs -f
    ```

3.  **Stop:**
    ```bash
    docker-compose down
    ```

## 🤝 Contributing

Pull requests are welcome. For major changes, please open an issue first to discuss what you would like to change.

## 📄 License

[GPL-3.0](https://choosealicense.com/licenses/gpl-3.0/)
