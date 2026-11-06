# transcoder_poc

取代 Zencoder 的自架 ffmpeg 轉檔服務 POC。

- 兩支 API：`POST /jobs`、`GET /jobs/{jobID}`
- 支援 VOD（HLS 三條串流 + master playlist + 兩個 MP4）與 Clip（單一 MP4，寬 1280）
- 產出寫到本地資料夾（代替 GCS），回傳的 URL 是 `file://` 路徑
- job 存在記憶體裡，重啟就會消失

## 本地測試教學

### 1. 安裝工具

需要 Go 1.24+，以及含 `libx264`、`aac` 的 `ffmpeg` / `ffprobe`。

```bash
brew install go ffmpeg

# 確認編碼器都在
ffmpeg -hide_banner -encoders | grep -E 'libx264|aac '
```

### 2. 準備測試影片

測試影片放在 `testdata/`，不進版控，用腳本產生：

```bash
scripts/testdata.sh          # 合成影片 40 秒、1080p、有音軌 → testdata/input.mp4（約 30MB，幾秒鐘）
scripts/testdata.sh long     # 合成影片 5 分鐘、1080p（約 580MB），方便觀察進度變化
scripts/testdata.sh noaudio  # 合成影片 20 秒、1080p、沒有音軌，測試無音軌的處理
```

### 3. 啟動 service

```bash
go build -o transcoder_poc .
./transcoder_poc -addr :8080 -output ./output -workers 2
```

看到下面這行就代表啟動成功：

```
INFO transcoder poc listening addr=:8080 output=/.../output workers=2 preset=medium
```

| flag | 預設 | 說明 |
|---|---|---|
| `-addr` | `:8080` | 監聽位址 |
| `-output` | `./output` | 產出根目錄（代替 GCS bucket） |
| `-workers` | `1` | 同時處理的 job 數 |
| `-queue` | `100` | queue 上限，滿了 `POST /jobs` 回 503 |
| `-preset` | `medium` | x264 preset，想加快本地測試可用 `veryfast` |

按 `Ctrl+C` 關閉，會等進行中的 ffmpeg 結束後才離開。

### 4. 送出 job 並觀察進度（腳本）

另開一個 terminal，用 `scripts/run_job.sh` 一次完成「建立 job → 每 2 秒查詢進度 → 完成後印出結果」：

```bash
scripts/run_job.sh vod  testdata/input.mp4 vod-001
scripts/run_job.sh clip testdata/input.mp4 clip-001
```

輸出範例：

```
==> POST /jobs
{"jobID":1,"urls":{"url":"file:///.../output/vod-001/playlist.m3u8","reencode640":"...","reencode1280":"..."}}
==> GET /jobs/1 (every 2s)
15:01:19 {"jobID":1,"type":"vod","state":"processing","progress":4.2,"durationSec":300}
15:01:37 {"jobID":1,"type":"vod","state":"processing","progress":33.1,"durationSec":300}
...
15:02:14 {"jobID":1,"type":"vod","state":"finished","progress":100,"durationSec":300}
==> elapsed 56s
```

- service 不在 `localhost:8080` 時，第 4 個參數可以指定 host，例如 `scripts/run_job.sh vod testdata/input.mp4 vod-001 localhost:18080`
- 用 `INTERVAL=1 scripts/run_job.sh ...` 調整輪詢間隔
- job 失敗時腳本會以非 0 結束，並印出 `error`

### 5. 送出 job 並觀察進度（curl）

想自己打 API 的話：

```bash
# 建立 VOD job（inputURL 要給絕對路徑或 http(s) URL）
curl -s -X POST localhost:8080/jobs \
  -H 'Content-Type: application/json' \
  -d '{"type":"vod","inputURL":"'"$PWD"'/testdata/input.mp4","outputFolder":"vod-001"}'

# 建立 Clip job
curl -s -X POST localhost:8080/jobs \
  -H 'Content-Type: application/json' \
  -d '{"type":"clip","inputURL":"'"$PWD"'/testdata/input.mp4","outputFolder":"clip-001"}'

# 查詢進度與結果（jobID 從 1 開始遞增）
curl -s localhost:8080/jobs/1

# 每 2 秒查一次
watch -n 2 'curl -s localhost:8080/jobs/1'
```

### 6. 檢查產出

```bash
# 檔案結構
find output/vod-001 -not -name '*.ts' | sort
ls output/clip-001

# 各產出的編碼與解析度
for f in output/vod-001/reencode-*.mp4 output/clip-001/*.mp4; do
  echo "$f: $(ffprobe -v error -select_streams v:0 -show_entries stream=codec_name,width,height -of csv=p=0 "$f")"
done

# master playlist 內容（應列出 640x360 / 960x540 / 1280x720 三種畫質）
cat output/vod-001/playlist.m3u8

# 播放
open -a "QuickTime Player" output/vod-001/playlist.m3u8   # HLS
open output/vod-001/reencode-1280.mp4                    # MP4
```

預期結果（以 1080p 輸入為例）：

| 檔案 | 規格 |
|---|---|
| `vod-001/playlist.m3u8` | master playlist，三種畫質 |
| `vod-001/file-{640,960,1280}.m3u8` + `.ts` | HLS，640x360 / 960x540 / 1280x720 |
| `vod-001/reencode-640.mp4` | 640x360 |
| `vod-001/reencode-1280.mp4` | 1280x720 |
| `clip-001/{隨機名稱}.mp4` | 1280x720 |

輸入比目標寬度小時不會放大，例如 720p 輸入的 960 / 1280 兩條都會維持原尺寸以下。

### 7. 其他情境

```bash
# 沒有音軌的影片
scripts/testdata.sh noaudio
scripts/run_job.sh vod testdata/input_noaudio.mp4 vod-noaudio

# 錯誤的參數 → 400
curl -s -X POST localhost:8080/jobs -d '{"type":"foo","inputURL":"x","outputFolder":"a"}'
curl -s -X POST localhost:8080/jobs -d '{"type":"vod","inputURL":"x","outputFolder":"../a"}'

# 輸入檔不存在 → job 會變成 failed，error 帶 ffprobe 的錯誤訊息
scripts/run_job.sh clip /no/such/file.mp4 clip-bad

# 不存在的 jobID → 404
curl -s localhost:8080/jobs/999
```

### 8. 清理

```bash
rm -rf output       # 轉檔產出
rm -rf testdata     # 測試影片
```

## API

### `POST /jobs`

建立 job，**同步**回傳 job ID 與輸出 URL。這支 API 不跑 ffmpeg，由 worker 非同步處理。

| 欄位 | 說明 |
|---|---|
| `type` | `vod` 或 `clip` |
| `inputURL` | 本地絕對路徑或 http(s) URL（ffmpeg 直接讀取） |
| `outputFolder` | 單一資料夾名稱（對應 vodID / clipID） |

回應：`{"jobID": 1, "urls": {"url": "...", "reencode640": "...", "reencode1280": "..."}}`（Clip 只有 `url`）

### `GET /jobs/{jobID}`

只讀取 worker 寫入的狀態，不跑 ffmpeg。

| 欄位 | 說明 |
|---|---|
| `state` | `queued` / `processing` / `finished` / `failed` |
| `progress` | 0～100。編碼 0～90，VOD remux 90～99，全部完成才會是 100 |
| `durationSec` | ffprobe 量到的影片秒數 |
| `urls` | 同 `POST /jobs` 的回應 |
| `error` | 失敗原因（僅 `failed` 時） |

## 處理流程（worker）

1. `ffprobe` 取得總秒數與是否有音軌
2. `ffmpeg` 轉檔，解析 `-progress pipe:1` 的 `out_time_us` 換算進度
   - Clip：`clipArgs`
   - VOD：`vodHLSArgs`（一次編碼三條 HLS + master playlist），再用 `vodRemuxArgs` 從 640 / 1280 的 HLS 重新封裝出 MP4
3. 標記 `finished`，progress = 100

## 檔案

| 檔案 | 內容 |
|---|---|
| `main.go` | flag、啟動 HTTP server 與 worker、graceful shutdown |
| `handler.go` | 兩支 API |
| `worker.go` | job 處理流程、進度分配、輸出 URL |
| `ffmpeg.go` | ffprobe、ffmpeg 執行與進度解析、VOD / Clip 指令 |
| `job.go` | Job model 與記憶體 store |
| `scripts/testdata.sh` | 產生 / 下載測試影片 |
| `scripts/run_job.sh` | 建立 job 並輪詢到完成 |

## 與正式服務的差距

- 上傳 GCS（public-read、Content-Type、`playlist.m3u8` 最後上傳）
- job 持久化（Mongo / MySQL）、worker 租約與重試
- 輸入檔先下載到本地（目前 ffmpeg 直接讀 `inputURL`）
- 認證、metrics、Clip 畫質參數對齊 Zencoder 產出
