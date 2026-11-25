# Kubernetes で動かす

このフォルダは kustomize の一式です。`kubectl apply -k` でそのまま入れられます。

| ファイル | 内容 |
|---|---|
| `namespace.yaml` | `ts2560` という Namespace |
| `deployment.yaml` | ダッシュボード本体（1台） |
| `service.yaml` | クラスタ内向けの Service（ポート 80 → 8560） |
| `pvc.yaml` | 履歴を保存するボリューム（100Mi） |
| `credentials.env.example` | 機器のアドレスとログイン情報のひな形 |
| `kustomization.yaml` | 上をまとめたもの。`credentials.env` から Secret を作る |

## 必要なもの

- kubectl 1.27 以降（kustomize が入っているもの）
- `ReadWriteOnce` のボリュームが作れること（既定の StorageClass があれば、そのまま使われます）
- Pod から次へ通信できること
  - TS-2560 の Web 画面（TCP 80）
  - TS-2560 の NTP（UDP 123）
  - 公開NTPサーバー（UDP 123）：既定は `ntp.nict.jp`、`time.google.com`、`ntp.jst.mfeed.ad.jp`。名前解決のため DNS も
- イメージは公開なので、pull 用の Secret は要りません（`ghcr.io/konoe-akitoshi/ts2560-dashboard`、amd64 / arm64）

## 入れる

```bash
git clone https://github.com/konoe-akitoshi/ts2560-dashboard.git
cd ts2560-dashboard/deploy/kubernetes
cp credentials.env.example credentials.env
```

`credentials.env` を開いて、機器のアドレスとログイン情報を書きます。このファイルは git に入りません。

```
TS2560_HOST=10.0.0.10
TS2560_USERNAME=********
TS2560_PASSWORD=********
```

中身を確かめてから入れます。

```bash
kubectl kustomize .
```

```bash
kubectl apply -k .
```

## 動いているか確かめる

```bash
kubectl -n ts2560 get pods,svc,pvc
```

```bash
kubectl -n ts2560 logs deploy/ts2560-dashboard
```

起動すると `TS-2560 dashboard: http://0.0.0.0:8560/ (機器のアドレス)` と出ます。

```bash
kubectl -n ts2560 port-forward svc/ts2560-dashboard 8560:80
```

ブラウザで http://127.0.0.1:8560/ を開きます。画面の上に「収集エラー」が出ていなければ、機器から読めています。

## よくあるつまずき

| 症状 | 原因と対処 |
|---|---|
| 「NTP(UDP 123) に応答がありません」 | Pod から機器の UDP 123 に届いていません。NetworkPolicy やファイアウォールを確認してください |
| 「Web画面の取得に失敗」 | Pod から機器の TCP 80 に届いていないか、`TS2560_HOST` が違います |
| 「ログインに失敗しました」 | `TS2560_USERNAME` / `TS2560_PASSWORD` が違います |
| 「基準時刻との差」がずっと空 | 公開NTPサーバーに届いていません。社内のNTPサーバーと比べたい場合は、下の「設定を変える」を見てください |
| Pod が `Pending` のまま | PVC が作れていません。`kubectl -n ts2560 describe pvc ts2560-data` で StorageClass を確認してください |

NetworkPolicy で外向きの通信を絞っている場合は、たとえば次のように許可します（アドレスは環境に合わせてください）。

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: ts2560-dashboard-egress
  namespace: ts2560
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: ts2560-dashboard
  policyTypes: [Egress]
  egress:
    - to:
        - ipBlock: { cidr: 10.0.0.10/32 }   # TS-2560
      ports:
        - { protocol: TCP, port: 80 }
        - { protocol: UDP, port: 123 }
    - ports:                                  # 公開NTPサーバー
        - { protocol: UDP, port: 123 }
    - ports:                                  # DNS
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
```

## 設定を変える

`deployment.yaml` の `env` に足します。使える設定は、リポジトリ直下の README の「設定」の表にあります。

```yaml
          env:
            - name: TS2560_REFERENCE_NTP
              value: ntp1.example.internal,ntp2.example.internal
```

## 外から見られるようにする

Service はクラスタ内向けです。外から見せる場合は Ingress を足します。**ダッシュボード自体にはログイン機能がない**ので、手前で認証をかけてください。

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: ts2560-dashboard
  namespace: ts2560
spec:
  rules:
    - host: ts2560.example.internal
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: ts2560-dashboard
                port:
                  name: http
```

## 更新する

既定では `latest` を使い、Pod を作り直すたびに最新のイメージを取ります。

```bash
kubectl -n ts2560 rollout restart deploy/ts2560-dashboard
```

決まった版に固定したい場合は、`kustomization.yaml` の `newTag` を、GitHub Actions が付けたタグ（`sha-` とコミットの先頭7文字、例：`sha-50daa38`）に書き換えてから `kubectl apply -k .` します。

## 消す

```bash
kubectl delete -k .
```

Namespace ごと消えるので、ボリュームにためた履歴も消えます。
