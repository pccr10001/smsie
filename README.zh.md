# smsie

`smsie` 是一套以 Go 撰寫的簡訊管理儀表板，支援同時管理多組 GSM/LTE 數據機、收發 SMS，並可透過 Webhook 與外部系統整合。

## 功能特色

- **數據機管理**
  - 自動掃描並偵測序列埠數據機。
  - 即時顯示訊號強度、電信商與註冊狀態。
  - 這些狀態屬於執行期資訊，不會作為資料庫的最終真實來源。
- **SMS 功能**
  - 可分頁、搜尋與檢視收到的簡訊。
  - 支援以 PDU 格式發送簡訊。
  - 收到 `+CMTI` 後可立即觸發掃描。
- **AT 指令終端**
  - 可直接對數據機送出原始 AT 指令，方便除錯與進階設定。
- **語音通話（撥號 / 掛斷）**
  - 每組數據機都有基本瀏覽器通話控制。
  - 追蹤通話狀態：`idle`、`dialing`、`in_call`。
  - 僅當 `AT+QCFG="usbcfg"` 探測到 UAC 已啟用時，前端才會顯示撥號介面。
  - 目前語音 UAC 流程以 Quectel 模組為主。
  - 瀏覽器端的 `Call` 一律先建立麥克風 + WebRTC signaling，再橋接到數據機 UAC 音訊，最後才送出 `ATD`。
  - PortAudio 與數據機音訊橋接只會在實際撥號時初始化。
  - 會自動進行 tty、USB 與 ALSA/UAC 裝置對應。
- **每數據機獨立 SIP Client / FXO 外線**
  - 每一組已就緒的 UAC 數據機都可以各自啟用 SIP client。
  - SIP 註冊與 listener 狀態由執行期管理，並依 ICCID 顯示。
  - 多組已就緒的數據機可同時維持多條 SIP 連線。
- **Webhook**
  - 可將收到的簡訊自動轉送到 **Telegram** 與 **Slack**。
- **使用者管理**
  - 角色權限控管（Admin / User）。
  - 密碼使用 **Bcrypt** 安全儲存。
  - 可限制每位使用者能存取的數據機。
- **資料庫支援**
  - 支援 **SQLite**（預設）與 **MySQL**。
- **現代化介面**
  - 使用 Bootstrap 與 jQuery 建構的響應式網頁介面。
- **跨平台**
  - 支援 Windows 與 Linux。

## 需求條件

- **Go 1.20 以上**
- **數據機驅動程式**
  - 請先確認系統已安裝對應的序列埠驅動。
- 已測試機種
  - Quectel EC20
  - Quectel EC800M
  - OpenLuat Air780E
- 已測試語音功能的機種
  - Quectel EC20

## 安裝方式

1. 下載原始碼

```bash
git clone https://github.com/pccr10001/smsie.git
cd smsie
```

2. 下載相依套件

```bash
go mod tidy
```

### Linux

3. 安裝建置相依套件並編譯

```bash
sudo apt install portaudio19-dev libusb-1.0-0-dev ffmpeg
go build # 加上 -tags nouac 可停用 UAC
```

### Windows

3. 安裝建置相依套件並編譯

```bash
# 建議在 mingw64 環境下建置
pacman -S mingw-w64-x86_64-ffmpeg mingw-w64-x86_64-portaudio mingw-w64-x86_64-libusb
go build # 加上 -tags nouac 可停用 UAC
```

## 設定檔

程式使用 `config.yaml`。若尚未建立，可參考以下範例：

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
    # PortAudio 裝置配對用的備援關鍵字
    # Quectel UAC 流程通常不需要手動設定
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

補充說明：

- SIP 帳號設定不放在全域 `config.yaml`。
- 每組數據機可在設定視窗中各自配置 SIP：
  - `enable`
  - `username`
  - `password`
  - `proxy`
  - `port`
  - `domain`
  - `transport`
  - `register`
  - `tls skip verify`
  - `accept incoming`
  - `invite target`
  - 選用的固定 `listener port`
- 全域 `calling.sip` 僅定義共用的執行期預設值，例如：
  - listener 起始埠
  - RTP 範圍
  - REGISTER 存活時間
  - INVITE timeout
  - DTMF 行為
- 若數據機不存在或 UAC 尚未就緒，smsie 會自動停止該數據機的 SIP client，並隱藏瀏覽器通話控制。
- 啟動時會自動執行資料庫 migration；舊版欄位 `s_ip_*` 也會自動改名為 `sip_*`。

## 語音通話（Quectel UAC）

- 探測數據機時，smsie 會送出 `AT+QCFG="usbcfg"` 來檢查是否支援 UAC。
- 會檢查 `+QCFG: "USBCFG",...` 最後 7 個旗標，且最後一個值必須為 `1`，才視為 UAC 已啟用。
- 只有 UAC ready 時，前端才會顯示瀏覽器通話 UI。
- 瀏覽器 `Call` 一律走 WebRTC 發起：
  - 先完成瀏覽器麥克風與 WebRTC signaling。
  - 再由後端初始化 PortAudio 與數據機 UAC 之間的音訊橋接。
  - 最後才送出 `ATD<number>;` 撥號。
- 若撥號失敗，後端會自動關閉 WebRTC session 與音訊橋接。
- USB / UAC 配對為自動化流程：
  - 先由數據機序列埠（`COMx` / `ttyUSBx`）回推出 USB 裝置。
  - 再透過 QCFG 回傳的 VID/PID 與 `gousb` 枚舉結果定位目標 UAC 裝置。
  - 在 Linux 上也會根據 tty 對應的 USB device path 回推正確的 ALSA `card` / `hw` 裝置。

## SIP Client / FXO 外線

- SIP client 是以數據機為單位設定，不是全域設定。
- 每組數據機都可設定：
  - `username`
  - `password`
  - `proxy`
  - `port`
  - 選用 `domain`
  - `transport`：`udp`、`tcp`、`tls`
  - `register`
  - `tls skip verify`
  - `accept incoming`
  - `invite target`
  - 選用固定 `listener port`
- SIP client 只會在該 ICCID 存在且 UAC ready 時啟動。
- 若數據機拔除或 UAC 不可用，smsie 會自動停止該數據機的 SIP client，前端也不會顯示 SIP / WebRTC 通話選項。
- 每組數據機都會維持自己的 SIP listener / registration，因此多組 UAC-ready 數據機可同時維持多條 SIP 連線。
- Listener port 規則：
  - 若數據機已設定 `listener port`，就固定使用該埠。
  - 若值為 `0`，smsie 會從 `calling.sip.local_port` 開始自動尋找可用埠，並回存到數據機設定。
- 外線用途的 SIP INVITE 行為：
  - 收到 SIP `INVITE <number>@<listener>` 時，會觸發數據機送出 `ATD<number>;`。
  - 音訊會橋接到數據機 UAC 裝置，不經瀏覽器 WebRTC。
  - PSTN 來電只有在 `accept incoming` 啟用且 `invite target` 不為空時，才會被轉送到 SIP。
- TLS 說明：
  - 對外 SIP client 的憑證驗證，可透過每數據機的 `tls skip verify` 關閉。
  - SIP TLS listener 預設使用執行期動態產生的自簽憑證；若需要正式憑證，需自行擴充部署。
- 本機 RTP 埠範圍由 `calling.sip.rtp_port_min` 與 `calling.sip.rtp_port_max` 控制。
- DTMF 面板與 API 的 DTMF 請求，在 SIP 通話中會轉成 `INFO` + `application/dtmf-relay`。
- 瀏覽器前端不會直接發起 SIP 通話；瀏覽器的 `Call` 一律走 WebRTC/UAC。SIP 發話主要用於後端/API 整合，或 VoIP 伺服器主動打到數據機 listener。

## 在數據機上啟用 UAC

- 在 AT 終端送出以下指令以啟用 UAC 裝置：

```text
AT+QCFG="usbcfg",0x2C7C,0x0125,1,1,1,1,1,1,1
```

- 若要啟用 UAC 並透過 USB 音效卡轉送 PCM：

```text
AT+QPCMV=1,2
```

## 在數據機上啟用 VoLTE

```text
# 啟用 IMS
AT+QCFG="ims",1

OK

# 查看目前 MBN 設定，`2,1,1` 代表已選用並啟用 `OpenMkt-Commercial-CT`
AT+QMBNCFG="List"

+QMBNCFG: "List",0,0,0,"ROW_Generic_3GPP",0x05010824,201806201
+QMBNCFG: "List",1,0,0,"OpenMkt-Commercial-CU",0x05011510,201911151
+QMBNCFG: "List",2,1,1,"OpenMkt-Commercial-CT",0x0501131C,201911141
+QMBNCFG: "List",3,0,0,"Volte_OpenMkt-Commercial-CMCC",0x05012011,201904261

OK

# 為了讓 IMS 註冊成功，通常需要改用 generic MBN profile
# 關閉自動選擇 MBN
AT+QMBNCFG="AutoSel",0

OK

# 停用目前 MBN
AT+QMBNCFG="deactivate"

OK

# 啟用 generic MBN
AT+QMBNCFG="select","ROW_Generic_3GPP"

OK

# 重新開機
AT+CFUN=1,1

# 再次檢查 MBN
AT+QMBNCFG="List"

+QMBNCFG: "List",0,1,1,"ROW_Generic_3GPP",0x05010824,201806201
+QMBNCFG: "List",1,0,0,"OpenMkt-Commercial-CU",0x05011510,201911151
+QMBNCFG: "List",2,0,0,"OpenMkt-Commercial-CT",0x0501131C,201911141
+QMBNCFG: "List",3,0,0,"Volte_OpenMkt-Commercial-CMCC",0x05012011,201904261

OK

# 檢查 IMS 狀態
AT+QCFG="ims"

+QCFG: "ims",1,1    # `1,1` 代表 IMS 已啟用

OK
```

## Linux 權限需求（通話 / UAC）

若要在 Linux 上使用語音功能，執行程序的使用者必須能存取序列埠、USB bus 與音訊裝置。

- 建議加入群組，例如：

```bash
sudo usermod -aG dialout,audio,plugdev <your-user>
```

- 請確認程式能存取：
  - `/dev/ttyUSB*`：AT 指令序列埠
  - `/dev/bus/usb/*`：`gousb` / `libusb` 用的 USB 枚舉裝置
  - PortAudio 會使用到的 ALSA / PulseAudio / PipeWire 裝置
- 如有需要，請依據數據機 `AT+QCFG="usbcfg"` 回傳的 VID/PID 建立對應的 udev 規則。

群組或 udev 規則修改後，請重新登入或重新開機。

## 資料檔

### `mcc_mnc.json`

程式會使用 `mcc_mnc.json` 將數字型 MCC/MNC 代碼轉成可讀的電信商名稱。你可以自行下載標準資料集，或使用以下格式建立：

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

## 使用方式

1. 啟動伺服器

```bash
./smsie
```

2. 開啟儀表板

使用瀏覽器打開 `http://localhost:8080`

3. 首次登入

- 第一次啟動且資料庫為空時，系統會自動建立預設 `admin` 帳號。
- 請查看主控台輸出的隨機密碼：

```text
WARN [...] INITIAL ADMIN CREATED. Username: admin, Password: <random-string>
```

- 請使用該帳密登入，並立即修改密碼。

## API 整合

smsie 同時提供儀表板使用的 REST API，以及基於 Streamable HTTP 的 MCP Server。

- **REST Base URL**：`/api/v1`
- **MCP Endpoint**：`/mcp`
- **驗證方式**
  - 儀表板 / 瀏覽器 REST API：`Authorization: Bearer <jwt>`
  - MCP Streamable HTTP：`Authorization: Bearer smsie_xxxxx...`
- **授權模型**
  - API Key 會繼承擁有者使用者的數據機存取範圍。
  - API Key 還會再受到自身權限旗標限制：
    - `can_view_sms`
    - `can_send_sms`
    - `can_send_at`
    - `can_make_call`
  - MCP tools 會沿用和儀表板相同的 ICCID 權限檢查，不會額外引入跨數據機的 IDOR 存取問題。

### API Key 管理

- `GET /apikeys`：列出 API Key
- `POST /apikeys`：建立 API Key，完整 `api_key` 只會回傳一次
- `POST /apikeys/:id/rotate`：旋轉既有 API Key，舊 key 會立即失效
- `DELETE /apikeys/:id`：刪除 API Key

建立範例：

```json
{
  "name": "mcp-bot",
  "can_view_sms": true,
  "can_send_sms": true,
  "can_send_at": false,
  "can_make_call": false,
  "expires_at": "2026-03-31T00:00:00Z"
}
```

### MCP Streamable HTTP

smsie 會在 `/mcp` 暴露一個真正的 MCP Server，使用 Streamable HTTP 與 JSON-RPC。

- 傳輸端點：`POST /mcp`、`GET /mcp`、`DELETE /mcp`
- 驗證：`Authorization: Bearer smsie_xxx`
- Session 模型
  - 先用 `POST /mcp` 初始化
  - 保存回應中的 `Mcp-Session-Id` header
  - `GET /mcp` 可開啟選用的 SSE stream
  - `DELETE /mcp` 可關閉 session
- 提供的 tools
  - `list_modems`
  - `list_sms`
  - `wait_sms`
  - `send_sms`

客戶端設定範例：

```json
{
  "mcpServers": {
    "smsie": {
      "type": "streamable-http",
      "url": "http://localhost:8080/mcp",
      "headers": {
        "Authorization": "Bearer smsie_xxx"
      }
    }
  }
}
```

初始化後的 tool call 範例：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "list_sms",
    "arguments": {
      "iccid": "YOUR_ICCID",
      "page": 1,
      "page_size": 20,
      "max_records": 100,
      "type": "received"
    }
  }
}
```

### 其他主要 REST 端點

- `GET /modems`：列出已連線數據機與執行期 worker / UAC / SIP 狀態
- `GET /modems/:iccid`：取得單一數據機資訊，包含每數據機 SIP 設定與狀態
- `PUT /modems/:iccid`：更新數據機名稱與每數據機 SIP 設定
- `DELETE /modems/:iccid`：刪除數據機設定檔（僅限 admin）
- `POST /modems/:iccid/at`：執行 AT 指令
- `POST /modems/:iccid/input`：送出原始輸入（例如 `^Z`）
- `GET /modems/:iccid/call/state`：查詢目前通話狀態、UAC ready 狀態與 SIP listener / register 狀態
- `GET /modems/:iccid/ws`：瀏覽器 WebRTC signaling WebSocket，token 以 `?token=` 傳入
- `POST /modems/:iccid/call/dial`：撥號；瀏覽器 UI 會在 WebRTC signaling ready 後送出 `{ "number": "09xxxxxxxx" }`
- `POST /modems/:iccid/call/hangup`：掛斷目前通話；若未指定 `via`，伺服器會自動選擇目前活躍的 call leg
- `POST /modems/:iccid/call/dtmf`：送出通話中 DTMF；body 例如 `{ "tone": "5" }`；若未指定 `via`，伺服器會自動選擇活躍通話路徑
- `GET /sms`：列出儀表板用的 SMS 資料

更完整的 API 定義可參考 `openapi/` 目錄或原始碼。

## 部署

### Systemd（Linux）

專案內含 `smsie.service`，可方便部署到使用 systemd 的 Linux 環境。

1. 將 binary 與靜態資源移到 `/opt/smsie`，或自行調整 service 檔路徑
2. 複製 service 檔

```bash
sudo cp smsie.service /etc/systemd/system/
```

3. 重新載入並啟用服務

```bash
sudo systemctl daemon-reload
sudo systemctl enable smsie
sudo systemctl start smsie
```

4. 查看日誌

```bash
journalctl -u smsie -f
```

### Docker

專案提供 `Dockerfile`，也可以直接使用 GHCR 的預建映像。

執行範例：

```bash
# 使用 GHCR 映像執行（已啟用自動掃描 serial port）
docker run -d -p 8080:8080 --name smsie \
  --privileged \
  --device=/dev:/dev \
  -v smsie_data:/app/data \
  ghcr.io/pccr10001/smsie:latest
```

若要使用自訂設定檔，可額外掛載：

```bash
-v $(pwd)/config.yaml:/app/config.yaml
```

### Docker Compose

專案內附 `docker-compose.yml`，預設使用 GHCR 映像。

1. 確認你已取得專案中的 `docker-compose.yml`
2. 啟動服務

```bash
docker-compose up -d
```

3. 查看日誌

```bash
docker-compose logs -f
```

4. 停止服務

```bash
docker-compose down
```

## 貢獻

歡迎提交 Pull Request。若是較大的改動，建議先開 issue 討論方向。

## 授權

[GPL-3.0](https://choosealicense.com/licenses/gpl-3.0/)
