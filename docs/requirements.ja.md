# yuruidb 要件整理

Goの同じAPIで、ローカルDB、リモートKVS、RDB、オブジェクトストレージを選べるようにする。検索をID指定と一覧に限定し、任意の検証済みIDを許可し、一覧はIDのバイト列昇順とする。NewIDはUUIDv7生成器として残す。更新しても一覧の位置は変わらない。

この文書は`.knowledge/concepts/`の人向け要約。**確定要件**はユーザーの依頼、**提案**は実装前の仮置きとして区別する。初期実装を追加済み。実装した選択肢・設定・制約は[実装ガイド](implementation.ja.md)に記録し、クラウド実サービスとの疎通は未検証。

## 確定要件

| 項目 | 要件 |
| --- | --- |
| 言語・提供形態 | Go 1.27以上の組み込みライブラリ。ジェネリクスを活用 |
| データ型 | JSONにシリアライズ可能なT。Openで接続し、Bind[T]とメソッド型引数でデータ種を選ぶ。Record[T]のID()・Revision()・Data |
| 操作 | insert、update、select、delete。外部キーによる関係管理は持たない |
| select | ID指定の取得と一覧取得 |
| 並び順 | IDのバイト列昇順。UUIDv7では時刻を反映した順、任意キーでは名前順 |
| 更新 | IDと作成順を保つ |
| 同時利用 | Lambdaのような独立プロセスから、共有リモートストレージを利用できる |
| オブジェクトストレージ | 楽観ロックなどで競合を扱う |
| 依存の軽さ | 可能ならHTTP APIを使い、SDKの一括バンドルを避ける |
| 拡張 | バックエンドはすべてstatic importで組み込む。追加機能もstatic importで拡張できる |
| ビルド | AWS、GCP、Cloudflareなどの環境タグが、対応バックエンドのstatic importをまとめて有効にする |

初期依頼の「更新順」は、追加回答の「作成順」で置き換えた。UUIDv7は時刻を含むIDだが、別プロセスでの同一ミリ秒内の生成順や、時計のずれを超えた厳密な確定順までは保証しない。後のbdf利用要件で任意キーを認め、IDの比較順を契約として明示した。[RFC 9562](https://www.rfc-editor.org/rfc/rfc9562.html)

## バックエンドの対象と制約

以下の対象に初期ドライバを実装した。bbolt・SQLiteは実DB、クラウド向けはHTTP fixtureで検証した。具体的な対応範囲と実サービス未検証の区別は実装ガイドに従う。

| 対象 | 初期の実装候補 | 制約・実装前の確認 |
| --- | --- | --- |
| bbolt | ローカルのトランザクションとキー順カーソル | 同じファイルの複数プロセス共有には使わない |
| DynamoDB | HTTP API、条件付き書き込み、collection＋IDのキー | パーティション設計、読取整合性、負荷 |
| その他KVS | 外部ドライバの追加 | CAS、条件付き作成、順序付き列挙の有無を明示 |
| RDB | `database/sql`とDB別ドライバ | 一般のRDBをHTTPだけで扱えるとは仮定しない |
| S3 | HTTP、固定IDをキーにした1レコード1オブジェクト | 条件付き操作、削除、ページング、バケット種別 |
| Cloud Storage | HTTP、generation条件付き操作 | 認証更新、列挙、削除、エラー対応 |
| Firebase | Firestore REST・Realtime Database RESTを別ドライバで実装 | 初期実装では両製品を提供。gcpタグは両方をimport |
| R2 | S3互換HTTP、条件付き操作 | S3との差分を個別に検証 |
| D1 | REST SQL、またはWorker経由のHTTP | 公式RESTは管理用途向け。実運用の経路を選ぶ |
| Cloudflare KV | 機能を限定したアダプタ、または別の調停機構と併用 | 単体で安全な競合更新を保証できない |

bboltの制約は[公式README](https://github.com/etcd-io/bbolt)、KVの競合特性は[公式の書き込み説明](https://developers.cloudflare.com/kv/api/write-key-value-pairs/#concurrent-writes-to-the-same-key)、D1の経路の制約は[公式チュートリアル](https://developers.cloudflare.com/d1/tutorials/build-an-api-to-access-d1/)に基づく。その他の根拠と未検証範囲は[調査メモ](backend-evidence.md)に保存した。

## 提案するAPIと競合の扱い

コレクション内の`Insert / Get / List / Update / Delete`を共通契約とする。追加要件で、Go 1.27以上、`Open`・`Bind[T]`・`Get[T]`等のメソッド型引数、JSON化できる`Data T`、`Record[T]`を採用した。IDとrevisionは非公開とし、`ID()`・`Revision()`で読み取る。初期の暫定的な`[]byte` APIはドライバ用の`RawStore`として残し、接続単位の共通`Store`でJSON変換する。型制約だけではJSON化を保証できないため、書き込み前にJSON変換と復元を検証する。

Insertは既存IDを上書きしない。UpdateとDeleteは取得時の不透明なrevisionを渡し、古いrevisionなら競合を返す案を推奨する。revisionと時系列IDは別物で、IDは固定、revisionは更新ごとに変わる。同じ内容を書き戻した場合や削除後に同じIDを使った場合にも、古い更新を通さない設計が必要になる。

レコード単位の安全性はバックエンドの原子的な条件付き操作で担保する。プロセス内mutexでは複数Lambdaの競合は防げない。条件を満たせないドライバは能力不足を明示し、安全な更新を要求した構成で黙って無条件上書きに切り替えない。

一覧はカーソル方式を初期案にする。ページ取得中に挿入・削除が起きる場合のスナップショット保証は約束しない。初期実装は昇順のみ。降順の必要性は今後の利用フィードバックで再検討する。S3の一般用途バケットはキー昇順なので、降順を必須にする場合はキー表現や取得方式の検討が要る。[ListObjectsV2](https://docs.aws.amazon.com/AmazonS3/latest/API/API_ListObjectsV2.html)

通信タイムアウトは「書き込まれなかった」とは限らない。自動再試行と冪等性の契約を決め、競合を最新revisionで勝手に再試行しない。

## static importと環境タグ

**バックエンドはすべてstatic importで組み込む。環境タグは、そのimport群をまとめる指定である。** 利用側が個別にimportする場合も、`aws`や`gcp`で対応するimport群を有効にする場合も、同じバックエンドパッケージを静的に組み込む。

具体的には、環境タグ付きのGoファイルに対応バックエンドのimportを列挙する。初期実装では`aws`がS3・DynamoDB、`gcp`がGCS・Firestore・Realtime Database、`cloudflare`がR2・D1・KVをimportする。

追加バックエンドは共通interfaceを実装してstatic importで接続できるようにする。初期実装では名前付きimportとコンストラクタ、blank importによるfactory登録の両方を提供する。タグ経由では共通のyuruidb.Configマップを使えるため、設定型を参照するための直接importも不要。値の変換などの追加機能も明示的なGoの合成で足す。具体的な追加機能は未指定。

提案ではタグ付きimport群を任意の組み立て用パッケージに集める。共通コアが全バックエンドをimportする構造は避ける。登録方式を採用する場合、タグ付きファイルの役割はstatic importとfactoryの登録までにし、import時に認証、ネットワーク通信、DB接続を開始しない。

| 指定例（提案） | 含めるもの |
| --- | --- |
| タグなし＋明示的import | 選んだドライバだけをstatic import |
| `-tags aws` | S3・DynamoDBのstatic import群。Lambdaの環境認証は共通のAWS署名実装で処理 |
| `-tags gcp` | GCS・Firestore・Realtime Databaseのstatic import群 |
| `-tags cloudflare` | R2・D1・KVのstatic import群。KVの能力制約も保持 |
| `-tags 'lambda,s3'` | S3だけ。Lambdaランタイムはアプリ側で用意 |
| `-tags 'gcs,r2'` | 複数環境の個別ドライバの併用 |

Lambdaは実行環境であり、ストレージではない。`lambda`単独でS3などを暗黙に選ばない。タグはstatic importの集合を選び、接続先や実際に使うバックエンドは明示設定で選ぶ。

たとえばS3を登録するファイルの条件は`//go:build s3 || aws`。Goが`aws`から`s3`を自動的に有効化するわけではない。複数タグは集合として合成し、同じfactoryの二重登録を避ける。直接importしたドライバには、組み立てパッケージのタグによる除外は効かない。[Goのビルド制約](https://pkg.go.dev/cmd/go#hdr-Build_constraints)

ビルド対象と依存取得も分けて考える。`go mod tidy`はほぼ全タグを考慮するため、タグだけで依存を隔離できるとはしない。提案は、外部依存を持たないコア、依存のあるドライバ用module、任意のタグ付き組み立て用moduleを分ける構成。初期はmodule数を抑え、依存が必要になった境界で分離する。[Go modules reference](https://go.dev/ref/mod#go-mod-tidy)

HTTP実装でも署名、認証更新、再試行の実装は必要になる。AWS SDK v2もサービス別moduleなので、薄いHTTP案と小さく選んだSDK案をバイナリサイズと保守負担で比較する。[AWS SDK v2 modularization](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/migrate-gosdk.html#modularization)

## 初期実装と残る判断

共通契約、bbolt、memory、SQL、S3、DynamoDB、GCS、Firestore、Realtime Database、R2、D1、KVを実装した。共通conformance、別プロセスのCAS競合、署名の公式ベクトル、ビルドタグの実行ファイル検査を実施。実クラウド・実PostgreSQL・性能測定は未実施。

Firebaseは初期実装で両製品を提供し、一覧は昇順、タグ構成はREADMEに示す内容を採用した。これらは実装上の選択として記録する。値は追加要件に従いジェネリクスとJSONに変更した。D1の実運用経路、KVの外部調停、降順、上限サイズ、性能目標、永続形式の移行は、利用・公開の判断時点で再検討する。


## bdfフィードバック後の確定事項

Storeは接続単位にし、Done・Waitingのような異なる名前付き型をBindで別コレクションへ対応づける。メソッド型引数のためGo 1.27以上を必須とする。同じ構造でもtype Done Jobとtype Waiting Jobなら区別し、公開のCollectionビューは持たない。

同一bboltファイルでの複数データ種、任意キー、条件付き物理削除を必須の利用条件として実装した。書き込み結果の照合、往復削減、bboltの二重base64除去、AWS role認証・地域解決、S3アクセス形式と互換性診断、一時応答の再試行、大きな文書、テスト用依存の分離も追加した。保存先ごとの能力差は実装ガイドに従う。クラウドの実サービス疎通や本番性能を確認済みとはしない。
