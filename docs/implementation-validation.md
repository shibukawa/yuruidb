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


## Go 1.26と型付きJSON API

最低バージョンをGo 1.26.0に変更。公開APIをOpen[T]・Store[T]・Record[T]{Id, Revision, Data}・Page[T]にし、共通層でJSON変換する。全ドライバにOpen[T]とOpenRawを用意し、factoryの低水準契約はRawStoreに分離した。保存エンベロープ、revision条件、static importの構成は維持した。

2026-10-20、macOS/arm64で以下を再検証した。

| 項目 | 結果 | 検証範囲 |
| --- | --- | --- |
| Go 1.26.0: go test -race ./... | 成功 | 最低バージョンで全テスト。各CRUD対応ドライバの型付きJSON、従来のraw conformance、別プロセスCAS、KVの型付き無条件操作を含む |
| Go 1.27.0: go test -race ./... | 成功 | 同じ全テスト |
| Go 1.26.0 / 1.27.0: go vet ./... | 成功 | ジェネリクスAPIを含む全パッケージの静的検査 |
| Go 1.26.0 / 1.27.0: scripts/check-builds.py | 成功 | タグ9構成、コア依存分離、CGOなしLinux/arm64クラウド4構成 |
| Go 1.27.0: go run ./examples/basic | 成功 | 型付きNoteとConfigマップによる実bboltのCRUD |
| Go 1.26.0: 別Go moduleからadapter.New[T]を利用 | 成功 | 公開APIだけを使う独自KVSの型付きinsert/update/get/list/delete |

store_test.goでは実bboltの型付き再オープンとページング、入力/戻り値のslice・map参照分離、JSONタグ、ポインタ、nil、map、RawMessage、カスタムJSONメソッドを確認した。関数・channel・NaN・循環参照・不正RawMessage・復元できないカスタムMarshalJSONの書き込みを拒否し、失敗したinsert/updateが保存データを変更しないことを確認。保存済みの不正JSON/型不一致でGetが失敗し、Listが部分ページを返さないことも確認した。

最初のsandbox内テストはHTTP fixtureのlisten権限で失敗したため、ローカルlistenを許可した環境で全テストを再実行し成功した。実クラウド・実PostgreSQLの検証範囲は追加していない。


## レコードメタデータの非公開化

Record[T]のid・revisionを非公開にし、ID()・Revision()で取得する。Dataは公開のまま維持し、Updateは新しいRecordを返す。JSONのid・revision・dataはMarshalJSON/UnmarshalJSONで維持する。JSON復元はRecord全体の置き換えで、復元エラー時には元の値を変更しない。ドライバ用RawRecordのフィールドは実装者が構築できるよう公開を維持する。

Go 1.26.0と1.27.0でgo test -race ./...が成功。Go 1.26.0でgo vet ./...とgo run ./examples/basicも成功した。record_test.goで公開の編集可能フィールドがDataのみであること、更新前Recordのメタデータを保持すること、JSON往復とJSON復元失敗時の元データ保持を検証した。各バックエンドの型付きconformanceとKVの型付き操作もアクセサ版で通過した。
