# ユーザーから受け取った要件

会話で明示された意図の保存用。技術的な提案は含めない。

## 初期依頼

> いろんなバックエンドに対応するストレージのライブラリをGoで作りたい。bbolt, dynamo, その他kvs、RDB、オブジェクトストレージなどが選べる。SDKをバンドルすると重いと思うので可能ならhttp apiを呼び出す様な薄い仕組みが良い基本的には外部キーなしのinsert, update, select(更新順ソートのみ), deleteがサポートできれば多くのストレージに対応できるはず。オブジェクトストレージは楽観ロックなどを駆使することを想定。lambdaのような多重で動くプロセスからも使えることを想定している

## 検索範囲の回答

「selectの絞り込みは、最初はどの範囲を想定していますか？」への回答：

> ID指定・一覧取得

## 並び順の修正

「更新順」の保証についての質問への回答：

> 作成順。キーはUUIDv7みたいな感じで

初期依頼の「更新順」は、この回答で「作成順」に置き換わった。厳密な分散コミット順への同意は受け取っていない。

## 要件整理と拡張方法の依頼

> knowledge-memory で要件を整理して。あと、バックエンドはstatic importで様々な要件が足せると良いし、aws(lambdaとs3など), gcp(cloud storage, firebase), cloudflare(R2, D1, KV)などの様なビルドタグでも指定できると良い

環境タグの正確な名前、含めるサービス、Firebaseの対象製品も未指定。

## バックエンド組み込み方法の明確化

> 基本的にバックエンドはすべてstatic import。aws, gcpなどのビルドタグではそれらのstatic importを行うみたいなイメージです

バックエンドの組み込みはすべてstatic importに統一する。環境タグは、その環境に対応するバックエンドのstatic importをまとめて有効にする。名前付きimportとblank importの使い分け、登録APIの形は未指定。

## 実装の依頼

> 実装して

この依頼に基づいて初期実装を進める。未指定のAPI細部は実装上の選択として記録し、ユーザーの明示要件と区別する。

## ライブラリ名とリポジトリ

> 名前をyuruidbとしたいです。git initしてgit remote addもしました

ユーザーが指定したライブラリ名はyuruidb。ローカルのgit remoteを確認し、originはhttps://github.com/shibukawa/yuruidb.gitであることを観察した。Go module pathはgithub.com/shibukawa/yuruidbに揃える。

## 初版の公開とドキュメント・ライセンス

> first versionをコミットしてpushしてください

> READMEは英語で、README.ja.mdは日本語でお願いします。ライセンスはapache2でお願いします

初版コミットとoriginへのpushが明示的に依頼された。README.mdは英語、README.ja.mdは日本語とし、プロジェクトのライセンスはApache-2.0を採用する。


## Goの最低バージョンと型付きJSONレコード

> 利用可能バージョンは現在アクティブなGo 1.26以上に限定し、ジェネリクスを活用したい。レコードはJSONへのシリアライズできるデータが欲しいはず。以下の様にできないか？

```go
yuruidb.Open[Data](ctx, "bbolt", yuruidb.Config{
    "Path": "records.db",
    "Collection": "notes",
})

type Record[Data] struct {
    Id string
    Revision string
    Data Data
}
```

最低対応バージョンはGo 1.26。公開APIは型引数でデータ型を選び、レコードはId・Revision・Dataを持つ。JSON化できるデータを対象とし、以前の暫定bytes選択を置き換える。JSON変換の検証方法や低水準ドライバAPIの分離は実装上の選択として記録する。


## レコードのメタデータ

> idなどはhiddenの方が良いか？変えることはないのでメソッドにしても良いと思う

RecordのID・revisionを非公開フィールドにし、読み取りメソッドで公開する。Goの命名に合わせてID()・Revision()とし、Dataは公開フィールドを維持する。更新は新しいRevisionを持つRecordを返す。メソッド名とJSON変換の互換維持は実装上の選択。


## bdf利用前のフィードバックと追加方針

利用予定プログラムから、同一bboltファイルの複数コレクション、名前キー、条件付き物理削除、応答喪失時の照合、Insert/Update/Listの往復数、bboltの二重base64、AWS role認証・地域・S3アクセス形式・再試行・互換性確認・大きな文書・テスト依存の分離が求められた。

ユーザーは複数データ種を1つのStoreで扱う方針を指定した。

> `store.Get[Data](id)`

このためGo 1.27専用への移行を許容した。さらに同じ構造の別コレクションは独立した名前付き型で分ける方針を指定した。

> DoneとWaiting、2つ型を作れば良い気はする。type Done Job, type Waiting Jobみたいな定義でもいいと思うので

最終的に、この方針で要件を満たせるなら実装を変更するよう依頼した。追加の接続・Blob・Revisionの仕様と実装範囲はREADMEと実装ガイドに記録する。

> 移行手順は不要です。まだバージョンもつけてない状態なので

未公開のため、旧形式の互換処理と移行手順は実装対象から外し、現行のAPI・保存形式に統一する。

## bdfからの追加フィードバック

2026-10-20、結果不明が競合にも一致する問題、S3互換性の確認用キーがレコード一覧を壊す問題、Blob機能の不足、bboltの稼働中バックアップ、DynamoDBのHTTP 400流量制限への対応を追加で求められた。

結果不明はErrConflictに一致させない方針を採用する。確認用キーはコレクション名に使えないドットで始まる予約prefixへ移し、キャンセルを引き継がないcontextで掃除する。Blobにはbbolt保存、io.ReaderAt、孤立候補を列挙するページ付き一覧、IDを再利用しない運用向けの明示的な無条件削除を追加する。bboltはStore.Backup(ctx, io.Writer)で全ファイルのスナップショットを出力する。IDの文字種は維持し、IPv6ホストや任意のプリンタ名の符号化はbdf側で行う。

ユーザーはRDBのバイナリ型もBlob保存先に含めるよう依頼し、「APIが統一されていることが優先」と指定した。既存SQLドライバのSQLite・PostgreSQLにOpenBlobs(ctx, Config)を追加し、同じ*blob.StoreでInsert・Open/ReadAt・List・Delete・Removeを提供する。単一のバイナリ列へ保存する実装を採用し、挿入時の全文メモリ保持は制約として明記する。

PR #1の後、遅いHTTPバックアップ出力中にGetがStoreのロックで待つ問題と、遅いbbolt Blob入力中に別レコードのUpdateが書き込みトランザクションで待つ問題が報告された。Backupは状態確認後にStoreのロックを放す。bbolt Blobは入力をトランザクション外で受け、作業用バケットへの分割書き込みと最後の一括公開で、ロック時間とメモリをバッチ単位に抑える。HTTP BlobのOpenはReadAtだけの利用で本文を取得しないよう、メタデータ取得と本文の遅延取得に分ける。

PR #2の後、bbolt自体のmmap拡張によってBackup中のUpdateが待つことが報告された。bbolt.ConfigにInitialMmapSizeを公開して初期対応づけを明示設定できるようにする。Windowsでは実ファイルも対応づけの大きさまで伸びるため、大きな既定値は設定しない。bdf側ではHTTPバックアップをローカル一時ファイルへ書き終えてから配信し、遅いネットワーク転送をDBの読み取りトランザクションから切り離す方針とする。
