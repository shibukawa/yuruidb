# yuruidb

[English](README.md) | [日本語](README.ja.md)

GoのジェネリクスでJSONデータを扱う、バックエンドを選べるレコードストレージライブラリ。ID指定のCRUDと、UUIDv7による作成順の一覧を提供します。バックエンドはすべてstatic importで組み込み、`aws`・`gcp`・`cloudflare`タグでimport群をまとめて有効にできます。

クラウドドライバは標準ライブラリのHTTPクライアントを使い、クラウドSDKを依存に持ちません。共通コアも外部パッケージをimportしません。bboltは選択したドライバが依存し、SQLiteのGoドライバはテストでのみ使います。

module pathは`github.com/shibukawa/yuruidb`です。Go 1.26以上に対応します。

## 使い方

個別に必要なドライバをimportします。blank importで登録され、接続はOpen時に行います。

```go
import (
    "context"
    "github.com/shibukawa/yuruidb"
    _ "github.com/shibukawa/yuruidb/drivers/bbolt"
)

type Note struct {
    Text string `json:"text"`
}

ctx := context.Background()
store, err := yuruidb.Open[Note](ctx, "bbolt", yuruidb.Config{
    "Path": "records.db",
    "Collection": "notes",
})
// 実際のコードでは各errを処理します。
defer store.Close()

id, err := yuruidb.NewID()
record, err := store.Insert(ctx, id, Note{Text: "hello"})
record, err = store.Update(ctx, id, Note{Text: "hello again"}, record.Revision())
record, err = store.Get(ctx, id)
page, err := store.List(ctx, yuruidb.ListOptions{Limit: 100})
err = store.Delete(ctx, id, record.Revision())
// record.ID(), record.Revision(), record.Data.Text
```

エラー処理を含む実行可能な例は[examples/basic](examples/basic/main.go)。`go run ./examples/basic`で試せます。各ドライバの名前付きimportと`Open[T](ctx, Config)`を使う方法もあります。

## JSONのデータ型

`T`には`encoding/json`で扱える構造体、ポインタ、map、slice、スカラー、`json.RawMessage`などを使えます。構造体タグと`MarshalJSON`・`UnmarshalJSON`にも従います。同じコレクションを複数プロセスから開く場合は、互換性のあるデータ型を使ってください。

任意の構造体を含む「JSON変換可能な型すべて」をGoの型制約では表せないため、制約は`any`です。書き込み前にJSONへの変換と`T`への復元を確認し、関数・channel・循環参照・NaNなどは`ErrInvalid`を返します。保存済みJSONが`T`に合わない場合はGet・Listがエラーを返し、Listは途中までのページを返しません。戻り値はJSONから復元するため、入力のポインタやmapをそのまま返しません。独自のJSONメソッド内の共有参照は、そのメソッドの実装に従います。`T = any`では通常の`encoding/json`と同様、JSONの数値は`float64`になります。

GoのID・revisionフィールドは非公開ですが、JSONには`{"id": "...", "revision": "...", "data": ...}`を出力します。JSONからの復元はメタデータを含むRecord全体の置き換えで、エラー時は元のRecordを維持します。非公開化はGoコードでの意図しない書き換えを防ぐもので、ストレージ側のrevision検証も引き続き行います。

## 環境タグ

アプリ側で`import _ "github.com/shibukawa/yuruidb/backends"`を追加し、`go build -tags aws`などで選びます。タグ付きGoファイルが対応ドライバをstatic importします。

| タグ | importされるドライバ名 |
| --- | --- |
| なし | なし |
| `aws` | `s3`, `dynamodb` |
| `gcp` | `gcs`, `firestore`, `firebase` |
| `cloudflare` | `r2`, `d1`, `cfkv` |
| 個別タグ | `s3`, `dynamodb`, `gcs`, `firestore`, `firebase`, `r2`, `d1`, `cfkv`, `bbolt`, `sql`, `memory` |

`firebase`タグはFirestoreとRealtime Databaseの両方をimportします。ドライバ名`firebase`はRealtime Databaseを指します。`firestore`タグはFirestoreだけを選びます。複数タグは併用でき、二重登録は起きません。

```sh
go run -tags aws ./examples/drivers
go run -tags 'gcs,r2' ./examples/drivers
```

タグ経由でもyuruidb.Configを使えるので、設定型のために個別ドライバを直接importする必要はありません。AWS環境では次の設定を渡せます。

```go
store, err := yuruidb.Open[Note](ctx, "s3", yuruidb.Config{
    "Bucket": "my-bucket",
    "Collection": "notes",
    "Region": "ap-northeast-1",
})
```

Lambdaは実行環境として想定しています。`lambda`タグ単独でストレージを選ぶ処理はなく、`-tags 'lambda,s3'`ではS3だけが組み込まれます。Lambdaのハンドラ・ランタイムはアプリ側で用意します。

## バックエンド

| ドライバ | 主な設定フィールド | 競合を検出するCRUD |
| --- | --- | --- |
| `memory` | `Collection` | 対応。プロセス内のみ |
| `bbolt` | `Path`, `Collection`, `Timeout` | 対応。ローカルファイル |
| `sql` | `DB`, `Dialect`, `Collection`, `AutoMigrate` | SQLite・PostgreSQLのSQL方言を実装 |
| `s3` | `Bucket`, `Region`, `Collection` | 条件付きPUT。一般用途バケット |
| `dynamodb` | `Table`, `Region`, `Collection` | 条件付きPutItem |
| `gcs` | `Bucket`, `Collection`, `Token` | generation条件付き操作 |
| `firestore` | `Project`, `Database`, `Collection`, `Token` | updateTime条件付き操作 |
| `firebase` | `Endpoint`, `Collection`, `Token` | Realtime DatabaseのETag条件付きPUT |
| `r2` | `Endpoint`, `Bucket`, `Collection`, `Credentials` | S3互換の条件付きPUT |
| `d1` | `AccountID`, `DatabaseID`, `Collection`, `Token`, `AutoMigrate` | revision条件付きSQL |
| `cfkv` | `AccountID`, `NamespaceID`, `Collection`, `Token` | 非対応。取得・一覧と明示的な無条件操作のみ |

HTTPドライバではClient・Authorize・Endpointも注入できます。スキーマ、認証、制約、検証範囲は[実装ガイド](docs/implementation.ja.md)を参照してください。

## 契約

- 値は`T`型のJSONデータ。Insert・Get・Updateは`Record[T]`、Listは`Page[T]`を返します。メタデータは`record.ID()`・`record.Revision()`で読み、編集可能なフィールドは`Data`だけです。更新結果は新しいRecordとして返します。IDはcanonical lowercase UUIDv7で、Insert前に生成します。更新でIDは変わりません。
- 一覧はID昇順。標準limitは100、範囲は1〜1000。返されたCursorが空になるまで継続します。
- UUIDv7は独立プロセス間の厳密な生成・コミット順を表しません。ページ間スナップショットも提供しません。
- Update・Deleteには取得したRevisionを渡します。古ければErrConflict。競合時に最新revisionで自動再試行する処理はありません。
- Deleteはrevisionを含むtombstoneを書きます。削除したIDは再作成でき、以前のrevisionは使えません。物理GCは未実装です。
- リモート書き込みでErrOutcomeUnknownが返る場合、書き込みは成功している可能性があります。同じIDを取得して結果を照合してください。
- CF KVではInsert・Update・DeleteがErrUnsupportedを返します。型付きStoreの`Set(ctx, id, data)`・`Remove(ctx, id)`で明示的に無条件操作を選べます。他のドライバではこれらのメソッドもErrUnsupportedを返します。

## 拡張と検証

独自ドライバはバイト列を扱う`yuruidb.RawStore`を実装し、`yuruidb.Register`で非ジェネリックのfactoryを登録します。共通の`Store[T]`がJSON変換を担当するため、ドライバはアプリのデータ型に依存しません。原子的なKVSから構築する場合は公開パッケージ[adapter](adapter/adapter.go)の`New[T]`・`NewRaw`でrevision・tombstone・カーソル処理を再利用できます。

共通パッケージと各ドライバの`OpenRaw`で低水準のバイト列APIを使えます。既存のRawStoreを型付きで扱うには`Wrap[T]`を使います。以前の生バイトデータは、`T`に対応したJSONであれば型付きAPIから読めます。バックエンドの保存エンベロープ形式は変更していません。

```sh
go test -race ./...
go vet ./...
python3 scripts/check-builds.py
```

bboltとSQLiteは実際のローカルDBで検証しています。クラウドドライバはローカルHTTP模擬サーバーでCRUD・条件付き書き込み・ページングを検証し、S3経路は別プロセス間の競合も検証しています。AWS署名は公式の既知ベクトルに照合しています。クラウド実サービス、実PostgreSQL、性能・料金・コールドスタートは未検証です。

要件の根拠は[要件整理](docs/requirements.ja.md)、構造化された現在状態は`.knowledge/concepts/`に保存しています。

## ライセンス

Copyright 2026 yuruidb contributors.

[Apache License 2.0](LICENSE)のもとで公開します。
