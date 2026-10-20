# Go 1.27のメソッド型引数による複数コレクション対応案

2026-10-20。bdfからのフィードバックと、`store.Get[Data](ctx, id)`へ変更する提案を受けた設計検討。追加方針として、同じ構造のデータも`type Done Job`・`type Waiting Job`のような別の名前付き型でコレクションを区別する。この文書は実装前の設計検討を保存したもの。現在は実装済みで、最終契約は[実装ガイド](implementation.ja.md)と[README](../README.ja.md)を参照。認証は追加SDKではなく標準ライブラリの役割認証チェーンを採用した。

**推奨は、接続先単位の非ジェネリックな`Store`と、データ型を指定するジェネリックメソッドへの移行である。** 1つのStoreがbboltファイルやS3バケットへの接続を所有し、コレクションごとの読み書きを振り分ける。`Record[T]`と`Page[T]`は維持する。

Go 1.27はメソッド独自の型パラメータを正式にサポートする。一方、interfaceのメソッドには型パラメータを宣言できず、ジェネリックメソッドで非ジェネリックなinterfaceメソッドを実装することもできない。公開Storeは具象型とし、ドライバ境界はバイト列を扱う非ジェネリックなinterfaceに置く。[Go 1.27 Release Notes](https://go.dev/doc/go1.27)、[Generic Methods](https://go.dev/blog/generic-methods)

**利用側の形**

```go
// 提案API。ctxとエラー処理は現行APIと同様に保つ。
type Waiting Job
type Done Job

store, err := yuruidb.Open(ctx, "bbolt", yuruidb.Config{
    "Path": "spool.db",
})
if err != nil {
    return err
}
defer store.Close()

if err := store.Bind[Waiting]("jobs"); err != nil {
    return err
}
if err := store.Bind[Done]("done"); err != nil {
    return err
}
if err := store.Bind[Printer]("printers"); err != nil {
    return err
}

waiting, err := store.Get[Waiting](ctx, jobID)
done, err := store.Get[Done](ctx, jobID)
printer, err := store.Get[Printer](ctx, "office-printer")
jobs, err := store.List[Waiting](ctx, yuruidb.ListOptions{Limit: 100})
```

公開メソッドは次の形を基準にする。`Insert`と`Update`は引数のdataからTを推論でき、`Get`・`List`・`Delete`は呼び出し時にTを指定する。`Delete`でも型引数が必要なのは、削除対象のコレクションを選ぶためである。

```go
func (s *Store) Bind[T any](collection string) error
func (s *Store) Insert[T any](ctx context.Context, id string, data T) (Record[T], error)
func (s *Store) Get[T any](ctx context.Context, id string) (Record[T], error)
func (s *Store) List[T any](ctx context.Context, opts ListOptions) (Page[T], error)
func (s *Store) Update[T any](ctx context.Context, id string, data T, revision string) (Record[T], error)
func (s *Store) Delete[T any](ctx context.Context, id, revision string) error
```

**型と保存名は分ける**

型引数だけでは、永続化に使う名前は決まらない。Goの型名・package pathから自動生成すると、リファクタリングで保存先が変わる。Storeごとの明示的な`Bind[T]("name")`を推奨する。アプリのデータ型にストレージ用メソッドを要求する必要もない。

| 規則 | 提案する挙動 |
| --- | --- |
| 対応の保持 | Storeごとの登録。プロセス全体のグローバル登録にはしない |
| 未登録のT | ErrInvalid。暗黙にコレクションを作らない |
| 再登録 | 同じT・同じ名前は冪等。Tの別名への付け替えは拒否 |
| 型識別 | 文字列化した型名ではなくreflect.Typeで識別。Tと*Tは同じ保存先に解決する方針とし、戻り値の型は呼び出しのTを維持する |
| 名前付き型 | DoneとWaitingは基になる構造が同じでも別の型として登録し、異なる保存先に解決する |
| コレクション名 | 現行ValidateCollectionの制約を継続 |
| スキーマ | 同じ保存名には互換性のあるJSONスキーマを使う。型引数は保存済みJSONのスキーマ検証を自動的に保証しない |
| 並行利用 | 登録と参照を同期し、一度決まった対応を変更しない |

`type Waiting Job`と`type Done Job`はそれぞれ独立した型なので、`Get[Waiting]`と`Get[Done]`だけで保存先を区別できる。公開APIはこの方式に絞り、コレクション名を呼び出しごとに指定するCollectionビューは設けない。1つのデータ型に1つの保存先という対応を保つ。

```go
// 取得したWaitingのデータから、明示的にDoneへ変換して保存する。
// メタデータとRevisionは新しい保存先で生成し直す。
done, err := store.Insert(ctx, jobID, Done(waiting.Data))
```

`type Done = Job`という型エイリアスは同じ型なので、別コレクションの識別には使えない。`type Done Job`では構造体のフィールドとJSONタグは保たれるが、Job自身のメソッドは引き継がない。Jobに独自のMarshalJSON・UnmarshalJSONを実装する場合は、各名前付き型でも必要な処理を定義する。

**接続の所有権と保存先の分離**

| 保存先 | Storeが共有するもの | コレクションの分離 |
| --- | --- | --- |
| bbolt | 1つの*bolt.DB | bucket |
| SQL / D1 | DB・実行器 | 現行テーブルのcollection列 |
| S3 / R2 / GCS | HTTPクライアント・認証・bucket設定 | `collection + "/"`というprefix |
| DynamoDB | HTTPクライアント・table設定 | pkのcollection値 |
| Firestore / Realtime Database | 接続設定・認証 | collection / パス |
| memory | 1つのデータ領域 | collectionとIDの組 |

S3のフォルダはキーのprefixとして表現する。例えば`jobs/<id>`と`printers/<id>`を使い、一覧には必ず末尾の`/`を含むprefixを渡す。`jobs`というprefixだけでは`jobs-old`も一致する。同じIDを別コレクションに保存しても干渉させない。

内部は「接続を所有する非ジェネリックなドライバ」と「コレクションを指定した軽いCRUDビュー」に分ける。現行recordstoreのエンベロープ・カーソル処理はビュー側に再利用できるが、各ビューがtransport.Closeを呼ぶ構造は変更する。所有者のStore.Closeだけが接続を閉じ、複数回のCloseは安全にする。SQLの借用DBは現行CloseDB設定の所有権を維持する。

1つのStoreから複数コレクションを使う際は、bbolt.Openを1回にできる。ただし、アプリの別の場所で同じパスを再度Openする問題は別に残る。bboltには正規化したファイルパス単位の接続管理と参照カウントを設け、最後のStore.CloseでDBを閉じる案を推奨する。相対パス・symlinkの扱い、設定の不一致、OpenとCloseの競合も仕様化する。コレクションビューは接続の参照数を増やさない。

カーソルと、今後保存先tokenを含めるRevisionは、接続先・コレクション・ID（Revisionの場合）に結びつける。別コレクションのカーソルやRevisionを渡しても処理を継続させない。

**bdfの指摘への対応順**

| 指摘 | 判断と対応 |
| --- | --- |
| 1. bboltの複数コレクション | 最優先。今回のStore再設計に含める。現行OpenRawは毎回bolt.Openを呼び、Closeは直接db.Closeを呼んでいる |
| 2. UUIDv7限定 | 最優先。ID検証とNewIDを分け、検証済みの名前キーを許可する。NewIDはUUIDv7生成ヘルパーとして残す |
| 3. 墓標の蓄積 | 最優先。原子的な条件付き削除を持つ保存先では削除APIを使う。持たない保存先だけ墓標にフォールバックする |
| 4. 書き込み結果が不明 | 次の優先事項。試行した操作・ID・コレクション・書き込みnonce・期待Revisionを型付きエラーで返す。可能な場合は読み直して自分の書き込みを確認する |
| 5. 往復数 | Insertの条件付き作成と、値も取得するListを先行。保存先tokenをRevisionに含めるUpdate最適化はエラー契約と合わせて行う |
| 6. bbolt容量 | 外側のItemのJSON/base64化をなくす。保存形式を識別できるコンパクトなバイナリ形式にする |
| 7. 運用面 | 下記の個別方針で進める。S3互換性の検証と再試行は競合更新の安全性に関わるため、単なる性能改善には分類しない |

任意キーを許す場合、一覧の共通契約は「IDのバイト列昇順」にする。UUIDv7では時刻を反映した順、プリンタ名では名前順になる。`"10"`は`"2"`より前に並ぶ。任意キーでも作成順を保証するには別の索引が必要で、S3のprefixだけでは実現しない。

許容文字と長さは全ドライバで点検する。例えばホスト名の`.`は、Realtime Databaseのキーには直接使えない。名前キーを受け入れるなら、ドライバ側で可逆なキー符号化と、論理IDの順序を保つ列挙を実装する必要がある。単にValidateIDだけ緩和すると共通契約が崩れる。[Firebaseのキー制約](https://firebase.google.com/docs/database/web/structure-data)

条件付き物理削除は妥当である。S3は2025年9月16日から一般用途バケットのIf-Match付き削除に対応し、GCSはgenerationを条件にできる。DynamoDB・SQL・bboltも保存先tokenの照合と削除を原子的に行える。遅れて到着した更新・削除が、削除後に再作成したレコードへ適用されないことを試験する。ライブラリの乱数Revisionに加え、実際のCASに使う保存先tokenも再作成前後で区別できることが条件になる。[S3の発表](https://aws.amazon.com/about-aws/whats-new/2025/09/amazon-s3-conditional-deletes-s3-general-purpose-buckets/)、[GCSの前提条件](https://docs.cloud.google.com/storage/docs/request-preconditions)、[DynamoDB DeleteItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DeleteItem.html)

既存の墓標は、Deleteの変更だけでは消えない。削除可能な保存先向けに、墓標のtokenを条件にする掃除処理も必要である。読み取り後に再作成された値を消してはいけない。S3のバージョニングではDeleteObjectがdelete markerを作るため、一覧の走査対象を減らすことと、古い版の容量を回収することは別になる。bboltも削除でページを再利用できるが、ファイル縮小には別途compactionが要る。[S3 DeleteObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_DeleteObject.html)

書き込み結果の照合では、保存中のnonceが試行したものと一致すれば成功を確認できる。不一致だけでは失敗とは断定できない。自分の成功後に別の更新が入った可能性がある。物理削除後の不在も、望む状態の確認はできるが、誰の削除が成功したかという履歴の証明にはならない。ctxの期限が切れた場合は、その場での自動照合を強行せず、呼び出し側が新しいctxで確認できる情報を返す。

Insertは新しいUUIDv7に限らず、任意の未使用IDでも条件付き作成から始められる。競合時だけ読み、既存の生きた値ならErrAlreadyExists、墓標ならそのtokenを条件に再作成する。Updateの事前GETを省くには、公開Revisionの形式だけでなくWriteの戻り値も変え、成功後の保存先tokenを取得する必要がある。応答不明後の再試行でも同じnonce・本文・条件を使い、新しいRevisionを勝手に採用しない。

Listは値を返せるドライバ向けの任意interfaceを追加する。DynamoDB Query・SQL SELECT・bbolt Cursorが返すpayloadとtokenを使い、Keys後の個別Readを避ける。現行FirestoreのrunQueryとRealtime DatabaseのGETも値を受信しているため、同様に再利用の対象になる。キーだけ取得できる外部adapterは従来経路を使う。

bboltはValueをEnvelopeのJSONで一度base64化し、そのEnvelope全体をItem.Dataとして再度JSON/base64化している。base64だけの倍率は約16/9で、ファイルサイズにはbboltのページ配置と拡張単位も効く。外側のbase64除去を先行し、生のバイト列に近い保存量を目指す場合はEnvelope自体のバイナリ化も検討する。

**運用面の個別方針**

| 項目 | 推奨 |
| --- | --- |
| AWS資格情報 | 現行Provider注入を維持し、EC2・ECS・EKSの一時資格情報と更新に対応する。自作チェーンの拡張より、任意依存のSDK v2認証adapterを第一候補にする。コアのSDK依存は増やさない |
| AWS地域 | 明示設定、AWS_REGION、AWS_DEFAULT_REGION、互換用の既定値の順に解決。S3とDynamoDBで共通化し、R2のautoは分ける |
| S3アクセス形式 | AWS向けはvirtual-hostedを既定候補にし、path-styleも明示選択可能にする。独自endpoint・R2・ドット入りbucketのDNS/TLS条件を分けて検証する。path-styleの廃止は延期されており、確定期限があるとは説明しない |
| 一時エラーの再試行 | 読み取りは上限付きbackoffとjitterを導入。書き込みは同じ操作の再送と結果照合を設計してから追加する。試行ごとに再署名し、contextとRetry-Afterを尊重する |
| S3互換サービス | create-if-absent、古いETagでの更新、並行更新、条件付き削除を使い捨てキーで検証する明示的な診断機能を用意。単にヘッダーを送るだけで保証済みとはしない。不対応時に無条件更新へ切り替えない |
| 大きな文書 | RawStoreは既に[]byteを扱うが、streaming APIはない。小さいメタデータをレコードに、大きな本文を別のBlob APIに置く案を分離して検討する。共通CRUDの上限は保存先別に明示する |
| テスト用cgo依存 | 純Goのmodernc.org/sqliteへ置換し、integration test用moduleへの分離も維持する。build tagだけではgo mod tidy上の依存隔離にならない |

AWSの既定認証チェーンと期限付き資格情報の更新は[公式説明](https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html)、S3のアクセス形式と廃止延期は[公式ガイド](https://docs.aws.amazon.com/AmazonS3/latest/userguide/VirtualHosting.html)を確認した。SDK認証adapterを別moduleにするかは、依存量と実装負担を比較して決める。

**実装と受け入れ条件**

実装順は、(1) Go 1.27・非ジェネリックStore・型と保存名の登録・接続所有権、(2) 任意キー・条件付き削除・既存墓標の掃除、(3) 結果照合・Insert/List最適化・bbolt保存形式、(4) token入りRevisionとUpdate最適化・クラウド運用の拡張を推奨する。bdfのbbolt利用開始は(1)(2)を必須とし、容量と一覧の劣化が実運用を妨げる規模なら(3)も含める。

未公開・バージョン未設定のため、互換性維持と移行手順は要件に含めない。Go 1.27のAPIと現行保存形式に統一し、README・examples・ドライバ・adapter・conformanceを揃える。

- 1つのbboltファイルでjobs・done・meta・printers・trustを同時に使い、再起動後も各データを読める。
- 同じIDを各コレクションに置き、Get・List・Update・Deleteとページングが混ざらない。
- 別のStoreが同じbboltファイルを使っていても、一方のCloseで他方を閉じない。Open/Closeの並行実行も確認する。
- 任意キー・Tと*T・未登録型・型の再登録・DoneとWaitingの保存先分離を確認する。同じ基になる構造でも別の名前付き型なら区別し、型エイリアスは同じ型として扱う。
- 2000件作成して1990件を削除した後、対応保存先では墓標分のReadが発生しない。既存墓標の掃除と再作成が競合しても生きた値を消さない。
- 古いRevisionによる更新・削除は再作成後も失敗する。削除前に読み終えた遅延writerも試験する。
- 書き込み成功後の応答喪失を再現し、照合成功、後続更新済み、ctx期限切れを区別する。
- Insertの成功は通常1回の条件付きWrite、値付きListはページごとに一覧取得だけで済むことを呼び出し回数で確認する。
- bboltバイナリ形式の保存量を、ファイル拡張単位だけに依存しない形で比較する。

同じStoreを使っても、jobsからdoneへの移動が自動的にトランザクションになるわけではない。S3を共通対象にするなら、bdf側には中断後に再開できる移動手順が必要になる。複数キーのトランザクションは今回のStore再設計とは別の能力として扱う。

実装前の調査時点では、既存コードはGo 1.27.0で`go test ./...`が全件成功した。通常のサンドボックスではHTTP fixtureのlistenが制限されたため、ローカルポート作成を許可した環境で再実行した。既存bboltテストはコレクションを順番に開いており、同時Openを検証していない。別の小さなコンパイル確認では、メソッド型引数、Insertの型推論、同じIDを異なる型の登録先に振り分ける構文を確認した。これはドライバ統合の検証ではなく、実サービスとの疎通・性能測定と新APIの実装は未実施。
