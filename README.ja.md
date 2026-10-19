# yuruidb

[English](README.md) | [日本語](README.ja.md)

Goで使う、バックエンドを選べるレコードストレージライブラリ。ID指定のCRUDと、UUIDv7による作成順の一覧を提供します。バックエンドはすべてstatic importで組み込み、`aws`・`gcp`・`cloudflare`タグでimport群をまとめて有効にできます。

クラウドドライバは標準ライブラリのHTTPクライアントを使い、クラウドSDKを依存に持ちません。共通コアも外部パッケージをimportしません。bboltは選択したドライバが依存し、SQLiteのGoドライバはテストでのみ使います。

module pathは`github.com/shibukawa/yuruidb`です。go.modはGo 1.23を宣言し、検証はGo 1.27で実施しています。

## 使い方

個別に必要なドライバをimportします。blank importで登録され、接続はOpen時に行います。

```go
import (
    "context"
    "github.com/shibukawa/yuruidb"
    _ "github.com/shibukawa/yuruidb/drivers/bbolt"
)

ctx := context.Background()
store, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{
    "Path": "records.db",
    "Collection": "notes",
})
// 実際のコードでは各errを処理します。
defer store.Close()

id, err := yuruidb.NewID()
record, err := store.Insert(ctx, id, []byte("hello"))
record, err = store.Update(ctx, id, []byte("hello again"), record.Revision)
record, err = store.Get(ctx, id)
page, err := store.List(ctx, yuruidb.ListOptions{Limit: 100})
err = store.Delete(ctx, id, record.Revision)
```

エラー処理を含む実行可能な例は[examples/basic](examples/basic/main.go)。`go run ./examples/basic`で試せます。各ドライバの名前付きimportと型付きConfig・Openを使う方法もあります。

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
store, err := yuruidb.Open(ctx, "s3", yuruidb.Config{
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

- 値は`[]byte`。IDはcanonical lowercase UUIDv7で、Insert前に生成します。更新でIDは変わりません。
- 一覧はID昇順。標準limitは100、範囲は1〜1000。返されたCursorが空になるまで継続します。
- UUIDv7は独立プロセス間の厳密な生成・コミット順を表しません。ページ間スナップショットも提供しません。
- Update・Deleteには取得したRevisionを渡します。古ければErrConflict。競合時に最新revisionで自動再試行する処理はありません。
- Deleteはrevisionを含むtombstoneを書きます。削除したIDは再作成でき、以前のrevisionは使えません。物理GCは未実装です。
- リモート書き込みでErrOutcomeUnknownが返る場合、書き込みは成功している可能性があります。同じIDを取得して結果を照合してください。
- CF KVではInsert・Update・DeleteがErrUnsupportedを返します。具象型のSet・Removeは無条件操作です。

## 拡張と検証

独自ドライバはyuruidb.Storeを実装し、yuruidb.Registerでfactoryを登録します。原子的なKVSから構築する場合は公開パッケージ[adapter](adapter/adapter.go)でrevision・tombstone・カーソル処理を再利用できます。

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
