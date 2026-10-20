# yuruidb

[English](README.md) | [日本語](README.ja.md)

Go 1.27以上で動く、保存先を選べるレコードストレージライブラリです。1つの`Store`が接続を所有し、メソッドの型引数でデータ種とコレクションを選びます。JSONデータは`Record[T]`・`Page[T]`で扱います。キーには検証済みの名前やUUIDv7を使え、一覧はIDのバイト列昇順です。

クラウドドライバは標準ライブラリのHTTPを使い、クラウドSDKに依存しません。コアも外部パッケージをimportしません。bboltは選択したドライバの依存、純GoのSQLiteテスト依存は別の[integration module](integration)に分離しています。

module pathは`github.com/shibukawa/yuruidb`です。

## 使い方

```go
import (
    "context"
    "github.com/shibukawa/yuruidb"
    _ "github.com/shibukawa/yuruidb/drivers/bbolt"
)

type Job struct { Text string `json:"text"` }
type Waiting Job
type Done Job

ctx := context.Background()
store, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{"Path": "spool.db"})
// 実際のコードでは各errを処理します。
defer store.Close()
err = store.Bind[Waiting]("jobs")
err = store.Bind[Done]("done")

id, err := yuruidb.NewID()
record, err := store.Insert(ctx, id, Waiting{Text: "hello"})
record, err = store.Get[Waiting](ctx, id)
record, err = store.Update(ctx, id, Waiting{Text: "updated"}, record.Revision())
page, err := store.List[Waiting](ctx, yuruidb.ListOptions{Limit: 100})
err = store.Delete[Waiting](ctx, id, record.Revision())
// record.ID(), record.Revision(), record.Data.Text
```

エラー処理を含む実行例は[examples/basic](examples/basic/main.go)です。各ドライバの`Open(ctx, Config)`も同じAPIを返します。`Bind`はI/Oを行わず、最初の操作で内部のコレクション処理を用意します。未登録型は`ErrInvalid`。型の保存先変更と、同じ保存名への別型の登録は拒否します。Tと*Tは同じ登録先を使います。`type Done Job`は別の型ですが、`type Done = Job`はエイリアスなので区別できません。名前付き型ではフィールドとタグは保たれ、Job自身のメソッドは引き継がれません。

## データと競合

TにはJSONにできる構造体、ポインタ、map、slice、スカラー、`json.RawMessage`、独自のJSON型を使えます。書き込み前にmarshalとunmarshalを行い、不正な値は`ErrInvalid`で拒否します。クライアント間では互換スキーマを使ってください。通常のmap・sliceは呼び出し元と戻り値で共有しません。Go上のメタデータは読み取り専用ですが、RecordのJSONにはid・revision・dataを保ちます。

- IDは1〜256文字のASCII英数字・`.`・`_`・`-`を受け入れます。`.`・`..`単体とFirestoreの予約名`__…__`は拒否します。大小文字は保ちます。`NewID()`は引き続きUUIDv7生成器です。UUIDv7でも別プロセス間の厳密な確定順は保証しません。名前キーはバイト列順なので`10`は`2`より前です。更新でIDは変わらず、ページ間スナップショットはありません。
- Insertは生きた値を上書きせず、まず条件付き作成を試みます。競合した場合だけ読みます。Update・Deleteには取得した不透明なRevisionを渡します。Revisionはコレクション・ID・乱数nonce・保存先のCAS tokenを含み、通常は更新前のGETを省けます。保存先tokenを返せないドライバでは必要に応じて読み直します。競合時に最新Revisionへ自動で乗り換えません。
- 対応保存先のDeleteは条件付き物理削除です。それ以外は条件付き墓標を書きます。同じIDを再作成しても古いRevisionは使えません。既存墓標には`store.PurgeTombstones[T](ctx)`を使えます。バケットの履歴容量やbboltファイルの縮小は別途管理します。
- bbolt・memory・SQL/D1・DynamoDB・Firestoreは一覧の値を再利用し、個別Readを省きます。S3/R2/GCSはキー列挙後に読み、Firebaseは個別のETagを取得します。一覧中の同時更新は観測される場合があります。
- bboltはsymlinkを解決したファイルパスごとにDBを共有し、最後の所有者がCloseしたときに閉じます。1ファイルの複数bucketを同時に使えます。値はbase64で包まずコンパクトなバイナリ形式で保存します。複数プロセス向けのリモート保存先ではありません。

## 書き込み結果が不明な場合

応答が失われても書き込みが成功している場合があります。レコード層はcontextと整合性が許す範囲で読み直し、試行したnonceが保存されていれば成功を返します。それ以外は`ErrOutcomeUnknown`を維持し、再送が条件競合になっても`ErrConflict`・`ErrAlreadyExists`には一致させません。結果を照合するまでは同じ変更を再適用しないでください。`errors.As`で`*yuruidb.MutationError`を取得できます。操作名・コレクション・ID・nonce・期待Revision・試行Revisionを含みます。

```go
var attempt *yuruidb.MutationError
if errors.As(err, &attempt) {
    current, readErr := store.Get[Waiting](recoveryCtx, attempt.ID)
    if readErr == nil && attempt.MatchesRevision(current.Revision()) {
        // 自分が試行した版が現在保存されている。
    }
}
```

不一致だけでは失敗とは断定できません。結果整合性のKVでは、一致してもその版を観測できたことを示し、全体の最新状態は保証しません。成功後に別の更新が入った可能性があります。物理削除後の不在も、望む状態の確認にはなりますが、誰の削除が成功したかの証明にはなりません。元のcontextが失効した場合は、呼び出し側が新しいcontextで照合します。

## 保存先と認証

| ドライバ | 主な接続設定 | 条件付きのレコード操作 |
| --- | --- | --- |
| memory | なし | プロセス内で対応 |
| bbolt | Path・Timeout | ローカルトランザクション |
| sql | DB・Dialect・AutoMigrate・CloseDB | SQLite/PostgreSQLのSQL方言 |
| s3 | Bucket・Region・Endpoint・AddressStyle | ETag条件 |
| dynamodb | Table・Region | 条件付きPutItem/DeleteItem |
| gcs | Bucket・Token | generation条件 |
| firestore | Project・Database・Token | updateTime条件 |
| firebase | Endpoint・Token | 削除を含むETag条件 |
| r2 | Endpoint・Bucket・Credentials | 検証済みS3条件 |
| d1 | AccountID・DatabaseID・Token・AutoMigrate | 条件付きSQL |
| cfkv | AccountID・NamespaceID・Token | 非対応。明示的な無条件Set/Removeのみ |

型付きStoreの接続設定ではCollectionを省き、Bindで登録します。OpenRawは従来どおり単一コレクションも指定できます。HTTPクライアントと認証は注入できます。AWSの既定認証は環境変数、web identity/STS（EKS IRSA）、コンテナ認証（ECS/EKS Pod Identity）、EC2 IMDSv2の順に探し、期限付き資格情報をキャッシュ・更新します。profile・SSOはProviderの注入で対応します。地域は明示設定、AWS_REGION、AWS_DEFAULT_REGION、us-east-1の順です。R2はautoを既定にします。

AWS S3は適切なbucket名ならvirtual-hostedを既定にし、ドット入りbucketはTLS互換性のためpath-styleを使います。AddressStyleにauto・virtual・pathを指定できます。独自endpointはpath-styleを既定にします。独自S3/R2 endpointのOpenでは有効な全コレクションの外側の`.yuruidb-probes/`に一時キーを作り、条件付き作成・置換・並行CAS・削除を検証して掃除します。後片付けにはcontext.WithoutCancelと10秒の期限を使い、失敗した場合はOpenも失敗します。キーが残ってもレコード一覧には混ざりません。予約prefixへの検証操作をIAMで許可してください。PUT条件を無視する保存先はOpenを失敗させます。DELETE条件に対応しない、または無視する保存先は墓標に切り替えます。AWS側も検証したい場合はVerifyConditions: trueを指定します。s3.Verify・r2.Verifyは診断結果を返します。有限の検証なので、すべての競合条件の保証を実証するものではありません。

一時的なHTTP 429/500/502/503/504には上限付きbackoff・jitterと再署名を使います。DynamoDBはHTTP 400のThrottlingException・ProvisionedThroughputExceededException・RequestLimitExceededも再試行します（[AWSのエラー仕様](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html)）。書き込みの再送対象は条件付き操作で、本文と条件は変えません。無条件KV書き込みとD1の変更操作は無条件に再試行しません。Retry-Afterの最小待機時間を尊重し、長い待機を求められた場合はエラーを返します。JSON応答上限は32MiBで、サービスの制限や符号化により実際の値はさらに小さくなります。S3/GCSでこの上限を超えるレコードは送信前に拒否します。

## 大きな文書

sql.OpenBlobs・bbolt.OpenBlobs・s3.OpenBlobs・r2.OpenBlobs・gcs.OpenBlobsで、明示したコレクションに変更しない文書を保存できます。Insert(ctx, id, io.Reader)は未使用のIDを作成します。HTTP版は署名と再送用に一時ファイルへ入力を退避し、文書全体をメモリへ読み込みません。bbolt版は同じファイルの予約バケット`.yuruidb-blobs/<collection>`へ64KiBずつ保存し、レコード用バケットから分離します。bboltの挿入は単一の書き込みトランザクションで行うため、確定まで入力分のメモリと書き込みロックを保持します。レコード用のJSON応答上限は適用しません。

Open(ctx, id)の戻り値はストリーミングに加えてio.ReaderAtにも対応します。読み終えるまでcontextを有効に保ち、Closeしてください。HTTPのReadAtは取得時のETag・generationを条件に範囲を読み、範囲指定を無視する保存先ではErrUnsupportedを返します。bboltは短い読み取りトランザクションを使います。List(ctx, yuruidb.ListOptions{Limit: 100})は本文を読まず、ID順のblob.Page（BlobsとCursor）を返します。各要素はID・Revision・Sizeを持ち、次ページにはCursorを渡します。レコードの参照と比較して孤立候補を探せますが、ページ間のスナップショットはありません。保存途中のBlobを削除しないよう、掃除と書き込みを呼び出し側で調整してください。

小さな型付きレコードには文書のID・版の参照を置きます。BlobのETagは同じ内容で再利用され得るため、IDは再利用せず新しく生成してください。条件付きDelete(ctx, id, revision)には保存先の対応が必要です。明示的なRemove(ctx, id)は条件付きDELETE非対応でも無条件に削除します。IDを再利用しない運用で使ってください。文書とレコードの操作をまたぐトランザクションはありません。

```go
blobs, err := s3.OpenBlobs(ctx, s3.Config{Bucket: "spool", Collection: "documents"})
meta, err := blobs.Insert(ctx, documentID, sourceReader)
reader, err := blobs.Open(ctx, meta.ID)
defer reader.Close()
buf := make([]byte, 4096)
n, err := reader.ReadAt(buf, 1024)
page, err := blobs.List(ctx, yuruidb.ListOptions{Limit: 100})
// 条件付き削除: blobs.Delete(ctx, meta.ID, meta.Revision)
// IDを再利用しない場合: blobs.Remove(ctx, meta.ID)
```

SQL版は既存のSQLドライバと同じConfigを受け取り、共通の*blob.Storeを返します。既定テーブルはururidb_blobsで、レコード用のururidb_recordsとは別です。Tableを指定する場合も別のテーブルにしてください。AutoMigrate: trueなら、SQLiteはBLOB列、PostgreSQLはBYTEA列を作成します。本文はJSONやbase64で包まずバイナリで保存します。OpenとListはメタデータだけを読み、Read・ReadAtはSQLのsubstrで必要なバイト範囲を取得します（[SQLite](https://www.sqlite.org/lang_corefunc.html)、[PostgreSQL](https://www.postgresql.org/docs/current/functions-binarystring.html)）。範囲を読むたびに乱数Revisionを確認し、IDの差し替えを検出します。SQL版の挿入は入力全体をメモリに読み込んでから単一のINSERTを行い、保存先DBのサイズ上限にも従います。

```go
// sqldriverはgithub.com/shibukawa/yuruidb/drivers/sql。
blobs, err := sqldriver.OpenBlobs(ctx, sqldriver.Config{
    DB: db, Dialect: "postgres", Collection: "documents", AutoMigrate: true,
    Identity: "my-app-primary",
})
// Insert/Open/ReadAt/List/Delete/Removeは他のBlob保存先と共通。
```

注入したSQL DBは既定で借用します。レコードとBlobで共有する場合はCloseDB: falseにし、両方を閉じた後でDBを閉じてください。異なるDBには安定した別のIdentityを付け、カーソルの流用を防いでください。レコードとBlobの操作はそれぞれ別のトランザクションです。

## bboltの稼働中バックアップ

store.Backup(ctx, writer)は、開いたままのbboltファイル全体を一貫したスナップショットとして出力します。全レコードのコレクションとBlobのバケットを含みます。writerのCloseとエラー処理は呼び出し側で行い、出力先は別ファイルにしてください。長いバックアップは読み取りトランザクションを保持し、ファイル拡張を待たせる場合があります。キャンセルはWriteの間で確認し、呼び出し側writerの停止中のWriteまでは中断できません。他の保存先はErrUnsupportedを返します。復元時は完成したスナップショットを別パスで開き、そのコピーにbboltのツールで詰め直しを行えます。レコードのRevisionはファイルパスに結び付くため、別パスへ復元した後は再取得してください。

```go
// storeはbboltで開いた接続、destinationは別のio.Writer。
err = store.Backup(ctx, destination)
```

## static importとビルドタグ

ドライバを個別にimportするか、`_ "github.com/shibukawa/yuruidb/backends"`と次のタグを使います。

| タグ | ドライバ |
| --- | --- |
| aws | s3・dynamodb |
| gcp | gcs・firestore・firebase |
| cloudflare | r2・d1・cfkv |
| 各ドライバ名 | 指定したドライバ |

firebaseタグはFirestoreも含みます。タグは合成でき、二重登録しません。lambda単独では保存先を選びません。`go run -tags aws ./examples/drivers`で組み込まれたドライバを確認できます。タグはimportの選択であり、実行時の資格情報・接続先は設定で指定します。

## 拡張・検証

[adapter.New](adapter/adapter.go)で原子的な保存先をレコードAPIへ接続し、`Bind[T](config.Collection)`を登録します。任意のItemWriter・Scanner・Deleter・CollectionTransportで、保存先token、値付き一覧、物理削除、共有コレクション処理を提供できます。OpenRaw・Wrap(raw)はバイト列の拡張境界です。

Firebaseの物理IDは、名前とバイト列順を扱うため数値にならないprefix付きhex符号化を使います。SQLで別の注入DBのカーソル・Revisionを区別する場合は、安定した異なるIdentityを指定してください。

```sh
go test -race ./...
go vet ./...
(cd integration && go test -race ./...)
python3 scripts/check-builds.py
```

両moduleともcgoは不要です。integration moduleだけが純Goのmodernc.org/sqliteとPostgreSQL用lib/pqに依存します。実bbolt・SQLiteと独立したHTTP fixtureで、応答喪失、再作成後の古い書き込み、複数コレクション、2000件から10件への削除、認証フロー、互換性診断、34MiBの文書を検証します。YURUIDB_POSTGRES_DSNを指定すると実PostgreSQLのBlobテストも実行します（[統合テスト](integration/README.md)）。実クラウド・PostgreSQLのレコードconformance・IAM・本番性能は未検証です。[実装ガイド](docs/implementation.ja.md)と[検証履歴](docs/implementation-validation.md)も参照してください。
