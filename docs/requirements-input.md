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
