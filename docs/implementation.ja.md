# 実装ガイド

## APIとstatic import

Go 1.26以上を対象とする。Store[T]はコレクションに紐づくInsert / Get / List / Update / Deleteを持ち、Record[T]のID()・Revision()・Dataでレコードを表す。各ドライバのOpen[T](ctx, Config)、または登録経由のyuruidb.Open[T](ctx, name, config)で接続する。登録経由では、ドライバへの直接importが不要なyuruidb.Configも使える。

設定マップのキーは各Configのフィールド名で、大文字小文字を区別しない。不明フィールド、型違い、同じフィールドの重複指定はエラー。Clientや認証コールバックも値として渡せる。durationには`"2s"`のような文字列も指定できる。

backends/import_*.goがタグに応じてドライバをblank importする。個々のドライバはタグなしでもstatic importで利用できる。登録時に接続や認証通信はしない。yuruidb.Drivers()で組み込まれた名前を確認できる。

共通コアは標準ライブラリだけをimportする。現在は単一moduleで、bboltの依存とテスト用SQLiteドライバもgo.modに記録する。タグはmodule依存を分離しない。module pathは設定済みoriginに合わせたgithub.com/shibukawa/yuruidb。

## JSONとジェネリクス

共通Store[T]がencoding/jsonでDataを保存用バイト列へ変換する。Record[T]は非公開のid・revisionと公開フィールドData T、Page[T]はRecords []Record[T]・Cursor stringを持つ。メタデータはID()・Revision()で読み、Updateは新しいRecordを返す。InsertとUpdateはdata Tを受け取る。型制約はanyで、書き込み前にmarshalとunmarshalを行い、JSONにできない値やTへ復元できない値にはErrInvalidを返す。JSONタグ、ポインタ、map、RawMessage、独自のJSONメソッドを利用できる。

読み取りはTへ復元し、不正なJSONや型の不一致はエラーにする。Listの復元失敗では部分ページを返さない。通常のJSON復元により、入力と戻り値のslice・map・ポインタの共有を避ける。独自UnmarshalJSONによる共有はその実装に従う。any内の数値はfloat64になり、JSONに出ない非公開フィールドも保持しない。同じコレクションでは互換スキーマを使う。

ドライバはRawStore・RawRecord・RawPageで既存のバイト列契約を実装する。Registerのfactoryは非ジェネリックで、アプリのTに依存しない。OpenRawとWrap[T]で独自の合成もできる。保存エンベロープ形式は維持し、JSONをそのpayloadへ格納する。以前の任意バイト列がJSONでなければ型付き読み取りには使えない。

RecordのJSON表現はid・revision・dataを保つ。MarshalJSON/UnmarshalJSONで非公開メタデータも変換し、復元時はRecord全体を置き換える。復元エラー時には既存Recordを変更しない。

## 認証とHTTP

AWSのS3・DynamoDBはauth/awsのSigV4署名を使う。既定ではAWS_ACCESS_KEY_ID、AWS_SECRET_ACCESS_KEY、AWS_SESSION_TOKENをリクエストごとに読む。Lambdaの環境認証情報で利用できる。共有profile、SSO、AssumeRoleチェーンは未実装。必要ならCredentialsコールバックかAuthorizeを注入する。

R2はアカウントのS3互換EndpointとCredentialsを明示する。Region標準値はauto。S3・R2はpath-styleアクセスを使う。S3 directory bucket、multipart upload、S3互換サービスすべての互換性は対象外。

GCS・Firestore・Realtime Database・D1・KVのTokenはauth.TokenProviderで、リクエストごとに取得できる。固定トークンにはauth.StaticToken(token)、GCP環境にはauth/gcp.Metadata(client)を渡せる。Metadataは期限付きトークンをキャッシュする。ローカルADC、サービスアカウントJWT、workload federationは呼び出し側の認証を注入する。

```go
// NoteはアプリのJSONデータ型。
// _ "github.com/shibukawa/yuruidb/backends" と "github.com/shibukawa/yuruidb/auth/gcp" をimportし、-tags gcpでビルド。
store, err := yuruidb.Open[Note](ctx, "gcs", yuruidb.Config{
    "Bucket": "my-bucket",
    "Collection": "notes",
    "Token": gcp.Metadata(nil),
})
```

Realtime DatabaseはOAuthのBearer認証を既定とする。Firebase ID tokenではIDToken: trueでauthクエリを使う。Endpointは対象DBのベースURL。FirestoreのEndpoint標準値はhttps://firestore.googleapis.com/v1、Database標準値は(default)。両製品は別ドライバとして実装した。

既定のHTTPクライアントは30秒でタイムアウトし、redirectを追わない。Clientを注入した場合は、そのtimeout・transport・redirect設定に従う。レスポンス上限は32MiB。実際のレコードサイズは符号化とサービス側の制限にも依存する。

自動再試行は行わない。送信後の通信断や5xxなどではErrOutcomeUnknownを返す。HTTPエラーには認証クエリ付きURLやレスポンス本文を含めない。全操作がcontextを受け取る。

## スキーマと接続先

S3・R2・GCSは既存バケットを使い、collection/UUIDv7をキーにする。コレクションのprefixはライブラリ専用とし、別形式のオブジェクトを混在させない。

DynamoDBのテーブルは利用側で作る。キーは文字列pkと文字列sk。pkにコレクション、skにcollection/UUIDv7、payloadにbinary、tokenに文字列を格納する。Get・QueryにはConsistentReadを指定し、GSIは使わない。コレクション単位のパーティションに負荷が集中する点には留意が必要。

SQLにはDB: *sql.DBとDialect: sqliteまたはpostgresを渡す。DBドライバはアプリ側でimportする。AutoMigrate: trueならテーブルを作り、既定では既存スキーマを使う。CloseDB: trueの場合だけStore.CloseがDBも閉じる。

既定テーブルは互換性のためururidb_recordsを使う。Config.Tableで別名を指定できる。列はcollection、id、payload、tokenで、主キーは(collection, id)。payloadはbase64文字列。idにはSQLiteのBINARYまたはPostgreSQLのC collationを指定する。既存スキーマを用意する場合も比較規則を揃える。テーブル名は単一の安全な識別子、値はパラメータとして扱う。

D1にはAccountID、DatabaseID、Tokenを指定し、SQLite用の条件付きSQLをREST query endpointへ送る。AutoMigrateも使える。公式RESTは管理用途向けでAPIレート制限があり、アプリ用Workerゲートウェイのデプロイはこのライブラリでは行わない。[Cloudflareの説明](https://developers.cloudflare.com/d1/tutorials/build-an-api-to-access-d1/)

bboltはファイルロックを使うローカルバックエンド。同じDBを複数Lambdaから共有する用途にはリモートバックエンドを選ぶ。標準Openタイムアウトは1秒、ファイルモードは0600。

## 競合・削除・一覧

共通処理は読み取り時のrevisionを比較し、最後の書き込みでもバックエンドのETag・generation・updateTime・token条件を指定する。プロセス内の比較だけに安全性を依存しない。

Deleteは条件付き書き込みでtombstoneを保存する。削除・再作成の挙動を揃えるため、ローカルDBでも物理削除しない。tombstoneで保存件数は増え続ける。物理GCは未実装で、稼働中のwriterと競合する直接削除は行わない。

UUIDv7はプロセス内で単調増加させ、時計が戻っても順序を保つ。独立プロセス間では時刻と乱数による順で、厳密な生成順・コミット順ではない。IDは小文字のcanonical形式。

カーソルはドライバ・コレクションと最後の走査キーを持ち、HTTPでは接続先、bboltではファイルパスも識別に使う。SQLでは接続先DB自体は識別しないため、別DBのカーソルを流用しない。値の更新で順序は変わらない。削除やtombstoneにより、カーソルがあっても次ページが空になる場合がある。過去のカーソルより前に追加されたIDは現在の走査に現れない。昇順のみで、ページ間スナップショットやoffsetは提供しない。

## Cloudflare KV

KVにはCASがないため、共通StoreのInsert・Update・DeleteはErrUnsupported。CapabilitiesのConditionalMutationsとStrongReadsもfalseになる。

無条件操作を使う場合は型付きStore[T]のSet・Removeを使う。登録経由でも同じメソッドを利用でき、他のドライバではErrUnsupportedになる。低水準のcfkv.OpenRawにもバイト列版を残す。Setは作成または上書き、Removeは無条件削除で、revisionを条件付き更新には使えない。地域間の可視性遅延や競合上書きを許容する用途に限る。[公式の整合性説明](https://developers.cloudflare.com/kv/api/write-key-value-pairs/#concurrent-writes-to-the-same-key)

KVのREST一覧はprefixから順に走査する。共通カーソル以降を取得するため以前のキーを再走査する場合があり、大規模な一覧向けに最適化していない。

## 拡張と検証範囲

独自バックエンドはRawStoreを実装してfactoryをRegisterする。原子的なKVSなら公開adapterパッケージで共通のレコード処理を再利用できる。BackendのRead・Write・Keys・Closeを実装し、Writeの空トークンはcreate-if-absent、非空トークンはCASとして原子的に評価する。Keysは対象collection/のキーを指定位置より後から昇順に返す。

共通conformance suiteは型付きJSONのCRUDと競合、低水準のbinary値、重複insert、revision競合、削除後の再作成、逆順insert後のID順一覧、tombstoneをまたぐページング、並行クライアント、引数・context・Closeを確認する。bboltは再オープン、SQLは実SQLite、D1のHTTP fixtureも裏側で実SQLiteを使う。

S3・R2・DynamoDB・GCS・Firestore・Realtime Databaseは、条件を原子的に評価するローカルHTTP fixtureで検証する。S3経路では別プロセス2個の競合も確認する。SigV4は[AWS公式の既知ベクトル](https://docs.aws.amazon.com/AmazonS3/latest/developerguide/sig-v4-header-based-auth.html)と照合する。KVは能力不足の明示と無条件操作を検証する。

scripts/check-builds.pyは各タグで実行ファイルを作り、登録ドライバの集合、コアの依存分離、CGOなしのLinux/arm64クラウドprofileビルドを確認する。

実クラウド、実PostgreSQL、IAM権限、性能・料金・コールドスタートは未検証。永続形式の移行機構も未実装で、公開APIと形式を固定する前の初期実装として扱う。
