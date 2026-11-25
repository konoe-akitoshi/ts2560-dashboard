# ts2560-dashboard

セイコーソリューションズのタイムサーバー **TS-2560** の状態を、1画面で見るためのダッシュボードです。

> 非公式のツールです。セイコーソリューションズ株式会社とは関係ありません。

機器の Web 画面にログインして状態画面（NTP状態・ピアリスト・GNSS状態・ハードウェア・ログなど）を読み、
NTP（UDP 123）で公開NTPサーバーとの時刻差も測って、1枚のページにまとめます。
Go の実行ファイル1つで動き、画面はその中に埋め込んであります。外部ライブラリは使っていません。

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
| | `interval` / `slow_interval` | `10` / `60` | 状態画面 / 機器情報・ログを読む間隔（秒） |
| | `max_stratum` / `max_ref_diff_ms` | `1` / `5` | これを超えたら注意を出す |
| | `jamming_rise` | `40` | 妨害電波の指標が、ふだんよりこれだけ上がったら注意を出す |
| | `gnss_holdover_hours` | `24` | 機器に設定した GNSS の同期維持時間 |

## API

| パス | 内容 |
|---|---|
| `GET /` | 画面 |
| `GET /api/status` | 収集結果のすべて（JSON） |
| `GET /healthz` | 死活確認（このサーバーが動いていれば 200） |

## 注意

- 機器の Web 画面の HTML を読んでいるので、ファームウェアの更新で画面が変わると読めなくなることがあります。動作を確かめたのはファームウェア 8.0.0 です。
- 「妨害電波の指標」は TS-2560 が報告している値をそのまま出しています。単位と、いくつから問題かの基準は確認できていないので、絶対値ではなく、ふだんとの差で見てください。
- 受信強度の「十分／やや弱め／弱い」などの境目は、目安として置いた値です。

## ライセンス

[MIT](LICENSE)
