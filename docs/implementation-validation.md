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

## Go 1.27・複数コレクションとbdfフィードバック対応

2026-10-20、Go 1.27.0、macOS/arm64で確認。最低バージョンを1.27.0へ上げ、Open/Storeは接続単位、Bind[T]とGet[T]等のメソッド型引数でコレクションを選ぶ形へ変更した。Done・Waitingを別の名前付き型として登録でき、公開Collectionビューは設けない。

| 検証 | 結果 | 範囲 |
| --- | --- | --- |
| sh scripts/check-all.sh | 成功 | 以下のrace・vet・integration・タグビルド・実行例を一括実行 |
| go test -race ./... | 成功 | 全ルートテスト。実bboltとクラウドHTTP fixture、メソッド型引数、CAS/ABA、結果照合 |
| go vet ./... | 成功 | ルートmoduleの静的検査 |
| integration: go test -race ./... / go vet ./... | 成功 | 別moduleに分離した実SQLiteとD1 fixture |
| scripts/check-builds.py | 成功 | 9タグ構成、コア依存分離、CGOなしLinux/arm64のクラウド4構成 |
| examples/basic | 成功 | Bind・メソッド型引数による実bboltのCRUD |
| CGO_ENABLED=0 go test ./... | 成功 | ルートmodule全体にcgo依存がないことを確認 |

multi_collection_test.goでは1つのbboltファイルでjobs・done・meta・printers・trustを同時に使い、同じIDの分離、Tと*T、別所有者のClose、再オープン、型再登録の拒否を確認した。multi_remote_test.goはS3/R2/GCS/DynamoDB/Firestore/Realtime Databaseで同じ型分離を検証する。

recordstoreの呼び出し回数テストでは、2000件作成して1990件削除した状態で残る項目が10件、Insert/削除でReadが0回、一覧がScan 1回・Read 0回、Updateも事前Read 0回であることを確認した。これは汎用レコード処理の回数の検証で、実サービスのレイテンシや課金測定ではない。

応答喪失のテストは、成功後の読み直しによる復旧、読み直しも失敗した場合のMutationError、後からのMatchesRevisionによる確認、後続更新済みの場合の不一致を検証した。墓標だけの外部transportへのフォールバック、既存墓標の掃除、掃除と再作成の競合、物理削除後の古い更新・削除も確認した。

未公開のため旧形式の互換処理と移行手順は設けず、bboltのバイナリ形式を直接検証した。1MiBのpayloadに対する保存値のオーバーヘッドは128バイト未満である。ファイルの割当単位・compaction・運用中の容量推移のベンチマークではない。

S3互換性のテストは条件を無視するendpointの拒否、DELETE非対応時の墓標フォールバック、AWS地域とURL形式を確認した。HTTPの試験は一時応答の再試行、再署名、同じ本文と条件の維持、応答不明後の競合、無条件変更を再送しないことを確認した。

AWS認証は環境変数の優先、コンテナ/Pod Identityのtoken file、期限前更新とキャッシュ、web identity/STS、IMDSv2、地域の優先順位をローカルfixtureで確認した。Blobは34MiBの署名付き送受信とハッシュ一致、重複作成の拒否、条件付き削除、GCS generationを確認した。

既存のHTTP fixtureは条件付き削除・値付き一覧の独立した処理へ拡張した。クラウド資格情報・実リソースを使う検証は行っておらず、実クラウド・実PostgreSQL・IAM・サービスの全互換性・本番負荷とコストは未検証。S3互換性probeも有限の観測である。

通常sandboxではローカルlistenとGoの外部キャッシュ書き込みに制約があるため、ローカルテスト用ポートとキャッシュ書き込みを許可した実行で最終チェックを完了した。

## 純Go SQLiteドライバへの置換

2026-10-20、Go 1.27.0、macOS/arm64で確認。integration moduleのmattn/go-sqlite3をmodernc.org/sqlite v1.60.1へ置換した。database/sqlのドライバ名とDSNを合わせ、接続ごとのbusy_timeoutとWALの指定を維持した。ルートmoduleの依存は変更していない。

| 検証 | 結果 | 範囲 |
| --- | --- | --- |
| sh scripts/check-all.sh | 成功 | ルートとintegrationのrace・vet、cgo無効のintegrationテスト、既存タグビルドと実行例 |
| CGO_ENABLED=0 sh scripts/check-all.sh | 成功 | 両moduleのテストとvet、既存タグビルドと実行例をcgoなしで実行 |

実SQLiteの同一ファイルに独立した2接続を開くconformanceと、実SQLiteでSQLを実行するD1 HTTP fixtureを通過した。race detector自体はcgoを要するため、cgo有効時だけ追加実行する。ドライバ変更による性能比較や実D1への接続は検証していない。

## 結果不明・probe分離・Blob・バックアップの追加対応

2026-10-20、Go 1.27.0、macOS/arm64で`sh scripts/check-all.sh`を通過した。ルートとintegrationのrace・vet、cgo無効のintegrationテスト、9種類のタグ構成、コア依存分離、Linux/arm64クラウド4構成、実行例を確認した。HTTP fixtureのローカルlistenを許可して実行し、Goビルドキャッシュは一時ディレクトリへ置いた。

- 応答喪失後の条件競合と照合失敗はErrOutcomeUnknownだけに分類し、ErrConflict・ErrAlreadyExistsには一致しない。MutationErrorとリモートエラーのerrors.As、contextエラーのerrors.Isを維持する。DynamoDBのHTTP 400流量制限3種は同じ本文・条件で再署名して再試行し、ValidationExceptionは再送しない。
- S3確認用キーの後片付けが権限で失敗しても、次に開いたコレクションの一覧へ混ざらない。キャンセル後の後片付けには、取り消しを引き継がず期限付きのcontextを渡す。
- bbolt・S3・R2・GCS共通でBlobの重複拒否、コレクション分離、本文なしのメタデータ一覧とページ送り、並行ReadAt、チャンク境界・末尾、版の差し替え検出、キャンセルとCloseを確認した。HTTPの範囲無視・不正なContent-Range・途中切断・サイズ不明時のEOFも検証した。条件付きDELETE非対応の保存先でも明示的なRemoveで削除できる。
- 稼働中のbboltをバックアップし、別パスで2つのレコードコレクションとBlobを復元した。元の接続はバックアップ後も更新できる。出力先の失敗と途中キャンセルはerrors.Isで判定できる。bbolt Blobの入力失敗は書き込み済みチャンクごとロールバックする。

bbolt Blobの挿入は単一の書き込みトランザクションで、確定まで変更分のメモリと書き込みロックを保持する。バックアップは読み取りトランザクションを保持する。これらの制約はREADMEと実装ガイドに記載した。実クラウド・IAM・本番負荷・大容量時の性能は今回も未検証。

## SQLのBlob API対応

2026-10-20、Go 1.27.0、macOS/arm64、実SQLiteと一時コンテナのPostgreSQL 18で確認した。drivers/sql.OpenBlobs(ctx, Config)は他の保存先と同じ*blob.Storeを返す。レコードとは別のururidb_blobsテーブルへ、SQLiteはBLOB、PostgreSQLはBYTEAで本文を保存する。

Blobの共通テストをinternal/conformance.RunBlobsへ移し、bbolt・S3・R2・GCS・SQLite・PostgreSQLに同じケースを適用した。ストリーミング、並行ReadAt、ページ送り、コレクション分離、条件付き削除、無条件削除、ID再作成後の古いReader、contextとCloseを確認した。SQL固有の検証では、全256種類のバイトを含む本文、バイト単位の範囲取得、空の文書、入力失敗時の不在、同じIDへの8並行挿入で成功が1件だけになること、同じ本文を再作成した後の古いRevisionでの削除拒否を確認した。SQLiteではレコードとBlobの同名IDの分離とDBの借用・所有も検証した。

YURUIDB_POSTGRES_DSNを設定したsh scripts/check-all.shを通過した。ルートとintegrationのrace・vet、cgo無効の統合テスト、9種類のタグ構成、コア依存分離、Linux/arm64のクラウド4構成、実行例を含む。PostgreSQL用lib/pq v1.12.3はintegration moduleのみに追加し、ルートgo.modは変更していない。通常の統合テストはDSN未設定ならPostgreSQLのケースだけをスキップする。

SQL版のInsertは本文全体をメモリへ読み込んでから1つのINSERTで作成する。OpenとListはメタデータだけを読み、Read・ReadAtはSQLのsubstrで範囲を取得する。DB固有のサイズ上限と、大容量時のメモリ・性能には別途運用上の検討が必要である。PostgreSQLのレコードconformanceは今回の検証範囲には含めていない。
