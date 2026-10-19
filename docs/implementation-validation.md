# 初期実装の検証記録

2026-10-20、Go 1.27.0、macOS/arm64で実施。クラウド資格情報・実クラウドリソースへのアクセスは行っていない。

| 項目 | 結果 | 検証範囲 |
| --- | --- | --- |
| go test -race ./... | 成功 | 全Goテスト。CRUD、CAS、ABA、一覧、削除、設定、Close、認証等 |
| go vet ./... | 成功 | 現在のパッケージの静的検査 |
| go run ./examples/basic | 成功 | blank import＋設定マップによる実bboltのCRUD |
| python3 scripts/check-builds.py | 成功 | タグ9構成の実行結果、コア依存、Linux/arm64のクラウド4構成 |
| 別Go moduleからadapterを使用 | 成功 | 一時moduleで公開APIだけを使う独自KVSのinsert/update |
| 実bbolt | 成功 | 共通conformance、再オープン、削除の永続化、コレクション分離 |
| 実SQLite | 成功 | 別DB接続2個による共通conformance |
| D1 HTTP | 成功 | REST fixtureの裏側で実SQLiteを実行 |
| S3 / R2 / DynamoDB / GCS / Firestore / Realtime Database HTTP | 成功 | 独立クライアント2個と、原子的な条件評価を行う模擬サーバー |
| 別プロセスのCAS | 成功 | S3 HTTP fixtureへ子プロセス2個。成功1件、競合1件 |
| SigV4の既知ベクトル | 成功 | AWS公式のGET object / lifecycle / listの3例 |
| Cloudflare KV | 成功 | CASを提供しないことの明示、Set/Get/List/Remove |

全クラウドの実サービス疎通、実PostgreSQL、IAM権限、性能・コスト・コールドスタートは未検証。HTTP fixtureの成功はサービスとのライブ互換性の証明ではない。降順、スナップショット、GC、移行、SSO/ADCチェーンは現在の実装範囲に含めていない。

## yuruidbへの名称変更後の確認

ライブラリ名・Goパッケージ名をyuruidb、module pathをgithub.com/shibukawa/yuruidbへ変更。go test -race ./...、go vet ./...、scripts/check-builds.py、basic例の実行が成功した。別の一時Go moduleから新module pathでコア・memory・adapterをimportし、CRUDの入口が使えることも確認した。SQLの既定テーブル名ururidb_recordsは既存データとの互換性のため保持した。
