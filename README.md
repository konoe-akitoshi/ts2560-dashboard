# ts2560-dashboard

セイコーソリューションズのタイムサーバー **TS-2560** の状態を、1画面で見るためのダッシュボードです。

> 非公式のツールです。セイコーソリューションズ株式会社とは関係ありません。

機器の Web 画面にログインして状態画面（NTP状態・ピアリスト・GNSS状態・ハードウェア・ログなど）を読み、
NTP（UDP 123）で公開NTPサーバーとの時刻差も測って、1枚のページにまとめます。
同じ結果を、JSON の API（OpenAPI 3.1）、Server-Sent Events、Prometheus のメトリクスでも出します。
Go の実行ファイル1つで動き、画面はその中に埋め込んであります。外部ライブラリは使っていません。

機器には `interval` 秒ごとに1回だけアクセスします。画面・API・Prometheus がいくつあっても、機器の負荷は増えません。
SNMP では取れない情報（衛星ごとの方角と受信強度、妨害電波、ntpd の時刻源の選択状況、クライアント一覧など）も扱えます。

## 見られるもの

- いまの状態と注意（Stratum、時刻源、電源やGNSSの異常など）
- 何か起きている間だけ出るお知らせ（GNSS捕捉中、GNSSを見失って維持中、内部時計で配信中）
- 直近12時間、どの時刻源に同期していたか
- 公開NTPサーバーと比べた時刻の差の推移
- ntpd の時刻源の選択状況（選択中・除外など）
- GNSS
  - 空：いま受信できている範囲（横から見た弧）と、これまでの方角ごとの受信の強さ（上から見た円）
  - 同期：内部時計とのずれの推移
  - 受信強度：衛星ごとの強さ
  - 妨害電波の指標：ふだん（中央値）との比較
- 機器情報、NTPリクエスト数、クライアント一覧、ログ

## 動かす

### 手元で

Go 1.25 以降が必要です。

```bash
go run . -demo            # 実機なしのデモデータで
cp config.example.json config.json   # host / username / password を書く
go run .                  # http://127.0.0.1:8560/
```

### Docker

イメージは GitHub Actions でビルドして `ghcr.io/konoe-akitoshi/ts2560-dashboard` に置いています（amd64 / arm64）。

```bash
TS2560_HOST=192.0.2.10 TS2560_USERNAME=... TS2560_PASSWORD=... docker compose up -d
```

### Kubernetes

```bash
cd deploy/kubernetes
cp credentials.env.example credentials.env   # 機器のアドレスとログイン情報を書く
kubectl apply -k .
kubectl -n ts2560 port-forward svc/ts2560-dashboard 8560:80
```

- 履歴はボリューム（PVC 100Mi）に保存するので、Pod は1台で動かします（`Recreate`）。
- Pod から機器（HTTP と UDP 123）と、公開NTPサーバー（UDP 123）に届く必要があります。NetworkPolicy で外向きの通信を絞っている場合は許可してください。
- 外から見せる場合は、Service に Ingress などを足してください。ダッシュボード自体にログイン機能はありません。

確かめ方、つまずきやすいところ、NetworkPolicy と Ingress の例、更新のしかたは [deploy/kubernetes/README.md](deploy/kubernetes/README.md) にまとめています。

## 設定

設定ファイル（JSON）と環境変数で指定します。両方あれば環境変数が優先です。

| 環境変数 | 設定ファイル | 既定値 | 内容 |
|---|---|---|---|
| `TS2560_HOST` | `host` | （必須） | 機器のアドレス |
| `TS2560_USERNAME` / `TS2560_PASSWORD` | `username` / `password` | | 機器の Web 画面のログイン情報 |
| `TS2560_BIND` / `TS2560_PORT` | `bind` / `port` | `127.0.0.1` / `8560` | 待ち受けるアドレスとポート（コンテナでは `0.0.0.0`） |
| `TS2560_REFERENCE_NTP` | `reference_ntp` | NICT・Google・MFEED | 時刻を比べる公開NTPサーバー（カンマ区切り） |
| `TS2560_DATA` | | `.`（コンテナでは `/data`） | 履歴を保存する場所 |
| `TS2560_CONFIG` | | `./config.json` | 設定ファイルの場所 |
| `TS2560_DEMO=1` | | | デモデータで動かす |
| `TS2560_LOG_FORMAT` | `log_format` | `text` | ログの形式。`json` にすると1行1件の JSON |
| `TS2560_CORS_ORIGIN` | `cors_origin` | （なし） | API を別のオリジンのページから読ませるとき（例: `*`） |
| | `interval` / `slow_interval` | `10` / `60` | 状態画面 / 機器情報・ログを読む間隔（秒） |
| | `max_stratum` / `max_ref_diff_ms` | `1` / `5` | これを超えたら注意を出す |
| | `jamming_rise` | `40` | 妨害電波の指標が、ふだんよりこれだけ上がったら注意を出す |
| | `gnss_holdover_hours` | `24` | 機器に設定した GNSS の同期維持時間 |

## API

読み取り専用です。仕様は `GET /api/openapi.json`（OpenAPI 3.1）にあります。Swagger UI などに読み込ませると一覧で見られます。

| パス | 内容 |
|---|---|
| `GET /` | 画面 |
| `GET /api/v1/status` | 収集結果のすべて（画面が使っているもの。`/api/status` も同じ） |
| `GET /api/v1/ntp` | ntpd の状態、時刻源（ピア）の選択状況、公開NTPサーバーとの差 |
| `GET /api/v1/gnss` | GNSS の状態（衛星・妨害電波・DOP・状態の遷移回数など） |
| `GET /api/v1/gnss/satellites` | 見えている衛星（方角・仰角・受信強度・追尾中か） |
| `GET /api/v1/sky` | 方角ごとの受信の記録 |
| `GET /api/v1/hardware` | 電源・温度・ファン・発振器 |
| `GET /api/v1/device` | 機種・ファームウェア・アドレス |
| `GET /api/v1/clients` | NTP クライアント一覧 |
| `GET /api/v1/requests` | 1時間ごとの NTP リクエスト数 |
| `GET /api/v1/log` | メッセージログ |
| `GET /api/v1/history` | 推移 |
| `GET /api/v1/alerts` | いま出ている注意 |
| `GET /api/v1/events` | 収集のたびに status を送る（Server-Sent Events）。画面もこれで更新しています |
| `GET /metrics` | Prometheus のメトリクス |
| `GET /healthz`・`/livez` | 生きているか |
| `GET /readyz` | 最初の収集が終わったか（終わるまで 503） |

- 応答には `ETag` が付きます。`If-None-Match` を付けて読むと、変わっていなければ 304 が返ります。
- エラーは RFC 9457 の `application/problem+json` で返します。まだ機器から読めていない資源は 503 です。

```bash
curl -s localhost:8560/api/v1/gnss/satellites
curl -sN localhost:8560/api/v1/events
```

## 監視（Prometheus）

`/metrics` を Prometheus で集めると、Grafana でのグラフや Alertmanager での通知に使えます。
主なメトリクスは次のとおりです（すべて `ts2560_` で始まります）。

| メトリクス | 内容 |
|---|---|
| `ts2560_up` | 機器の状態画面をすべて読めたら 1 |
| `ts2560_ntp_probe_stratum`・`ts2560_ntp_probe_leap_indicator` | クライアントに配っている Stratum とうるう秒指示子（3 は非同期） |
| `ts2560_ntp_sync_source{source}` | ntpd の同期先（gnss・ojjy・server・local・none など）が 1 |
| `ts2560_reference_offset_seconds` | 公開NTPサーバーとの差（正なら機器が進んでいる） |
| `ts2560_ntp_peer_offset_seconds`・`_status`・`_reach_ratio` | 時刻源ごとの差・選択状況・到達率 |
| `ts2560_gnss_state{state}` | GNSS の状態（Init・Sync・Holdover・Unsync など）が 1 |
| `ts2560_gnss_satellites_tracked`・`_visible`・`ts2560_gnss_snr_max` | 衛星の数と最大の受信強度 |
| `ts2560_gnss_satellite_snr{system,prn}` ほか | 衛星ごとの受信強度・仰角・方位・追尾 |
| `ts2560_gnss_jamming_strength{band}` | 妨害電波の指標（単位不明。ふだんとの差で見る） |
| `ts2560_gnss_antenna_ok`・`ts2560_gnss_spoofing_detected` | アンテナの状態、スプーフィング |
| `ts2560_hardware_normal{component}` | 電源・ファン・発振器・温度などが Normal なら 1 |
| `ts2560_temperature_celsius{sensor,location}` | 内部の温度 |
| `ts2560_device_info`・`ts2560_dashboard_build_info` | 機種・ファームウェア、このダッシュボードの版 |

Prometheus の指針では、エクスポーターはスクレイプのたびに相手を読むのが基本です。この機器は Web 画面へのログインと何枚もの画面の読み込みが必要で重いため、収集は `interval` 秒ごとの1回にまとめ、`/metrics` はその結果を返します。値がどれだけ新しいかは `ts2560_last_collect_timestamp_seconds` で分かります。

Kubernetes 用の ServiceMonitor と、手引きの目安に合わせたアラートのルール（PrometheusRule）を [deploy/kubernetes/monitoring](deploy/kubernetes/monitoring) に置いています。

## 注意

- 機器の Web 画面の HTML を読んでいるので、ファームウェアの更新で画面が変わると読めなくなることがあります。動作を確かめたのはファームウェア 8.0.0 です。
- 「妨害電波の指標」は TS-2560 が報告している値をそのまま出しています。単位と、いくつから問題かの基準は確認できていないので、絶対値ではなく、ふだんとの差で見てください。
- 受信強度の「十分／やや弱め／弱い」などの境目は、目安として置いた値です。

## ライセンス

[MIT](LICENSE)
