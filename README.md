# sokki — 会議音声のローカル文字起こし

仮想オーディオデバイス（Loopback / BlackHole）に流れる会議音声を、この Mac 上の
whisper.cpp で文字起こしします。結果はターミナルに逐次表示し、会議ごとにテキストファイルへ保存します。

- 推論はローカルの **whisper.cpp** (`whisper-server`) を外部プロセスとして使用します
- 音声認識は `127.0.0.1` の whisper-server で処理します。**既定で有効な自動要約では、確定した会議テキストが設定した AI プロバイダーへ送られます**
- 入力は設定したデバイスのみです（既定のマイクには切り替えません）。自分の声がそのデバイスに流れていれば、自分の声も文字起こしの対象になります
- **音声ファイルは保存しません**（テキストのみ）
- 入力デバイス・モデル・言語・保存先は **YAML 設定ファイル**で変更できます

**Loopback を持っていない場合**は、無料の **BlackHole 2ch** が使えます
（→「[BlackHole 2ch を使う（無料）](#blackhole-2ch-を使う無料)」）。

## ⚠️ 実行前に必ず設定してください（会議音声が聞こえなくなるのを防ぐ）

**Loopback を使う場合**、既定では **「Mute when capturing」**（キャプチャ中は消音）が
有効です。このままキャプチャすると、
**会議音声が自分のスピーカー／ヘッドホンから聞こえなくなります**。

（BlackHole を使う場合は代わりに **Multi-Output Device** の設定が必要です。
→「[BlackHole 2ch を使う（無料）](#blackhole-2ch-を使う無料)」）

> 実機確認中に実際にこの事象が発生しました（キャプチャ開始直後に会議音声が消え、
> 停止すると戻る）。本ツールは入力を開くだけで出力デバイスの設定は一切変更しませんが、
> Loopback 側の挙動により再生が止まります。

### Loopback の設定手順

1. **Loopback** を開く
2. 左の一覧から会議音声を流している仮想デバイス（既定は `Conference Output`）を選ぶ
3. **Sources** にある会議アプリ（Teams / Zoom 等）のタイルを選ぶ
4. タイル右上の **⚙ (Options)** → **「Mute when capturing」のチェックを外す**
5. 会議アプリ側の出力が自分に聞こえることを確認してから本ツールを起動する

**別解**: 仮想デバイスの **Monitors** に自分のスピーカー／ヘッドホン
（例: `Jabra Elite 10`）を追加しておくと、キャプチャ中もモニタ経由で聞こえます。
ただし Loopback の公式ドキュメントによると Monitor 経由は**再生遅延が加わる**ため、
まず手順 1〜5 の「Mute when capturing をオフ」を推奨します。

> 会議アプリを複数使う場合（Teams と Zoom など）は、**Source ごとに個別に**
> オフにする必要があります。

会議中に初めて使う場合は、**会議が始まる前に上記を設定して一度動作確認**してください。

## 必要なもの

| 依存 | 確認コマンド | 備考 |
|---|---|---|
| Go 1.26+ | `go version` | ビルドのみ |
| ffmpeg | `ffmpeg -version` | 音声取り込み (`brew install ffmpeg`) |
| whisper.cpp | `whisper-server -h` | 推論 (`brew install whisper-cpp`) |
| ggml モデル | 下記参照 | `ggml-large-v3-turbo.bin` |
| 仮想オーディオデバイス | `./bin/sokki list` | **Loopback**（有償）または **BlackHole 2ch**（無料）|

モデルは既定で次の順に探します（Superwhisper がダウンロード済みのものを優先）。

1. `~/Library/Application Support/superwhisper/ggml-large-v3-turbo.bin`
2. `~/.local/share/talknote/models/ggml-large-v3-turbo.bin`

> Superwhisper のモデルは `models/` サブディレクトリではなく**アプリサポート直下**に
> 置かれます（`models/` 配下は argmaxinc の CoreML 形式で別物のため候補にしません）。
> どちらも無い場合は設定ファイルの `transcription.model` か `--model` で
> ggml 形式 (`.bin`) を明示してください
> （→「[モデルを持っていない場合](#モデルを持っていない場合)」）。

## ビルド

```sh
git clone https://github.com/konono/sokki.git
cd sokki
make build        # bin/sokki を作る
```

## 使い方

```sh
./bin/sokki list   # まず入力デバイス名を確認
make run           # または ./bin/sokki
```

AI から使う場合は「[AI と連携する（MCP）](#ai-と連携するmcp)」を参照してください。

終了は `Ctrl-C` です。終了時にファイルを閉じ、保存先を表示します。

```
入力デバイス : [6] Conference Output
モデル       : /Users/<name>/Library/Application Support/superwhisper/ggml-large-v3-turbo.bin
言語         : ja
保存先       : transcripts/2026-10-16_095529.txt
録音を開始しました。Ctrl-C で終了します。
----------------------------------------
[00:00:03] 本日の会議を始めます
… [00:00:07] 議題は来週の                 ← 話している途中（約 1 秒ごとに書き直される）
```

発話中は暫定行が 1 行のまま書き換わっていきます。

```
… [00:00:07] 議題は                        約 1 秒後
… [00:00:07] 議題は来週の                  約 2 秒後
… [00:00:07] 議題は来週のリリース 経済      約 3 秒後（まだ誤りを含む）
… [00:00:07] 議題は来週のリリース計画について 約 4 秒後
[00:00:00] 議題は来週のリリース計画についてです。   ← 連続音声を約 12 秒で区切って保存
```

- **確定結果**（時刻付きの通常行）だけがファイルに保存されます
- **暫定結果**（`…` で始まる灰色の行）は表示のみで、保存されません
- 暫定文は**後続の音声を受けて書き直されます**。確定時には同じ発話区間を
  全体と短い区間の両方で認識するため、暫定文と内容が異なる場合があります

### 保存先

`transcripts/YYYY-MM-DD_HHMMSS.txt` に会議 1 回分を保存します。

```
# 会議文字起こし  開始: 2026-10-16 09:55:29
[00:00:03] 本日の会議を始めます
[00:00:07] 議題は来週のリリース計画についてです
# 終了: 2026-10-16 10:42:11  (124 行)
```

各行の時刻は **会議開始からの経過時間** です。1 行ごとに `flush` するため、
異常終了しても直前までの内容が残ります。

## 設定ファイル

毎回フラグを指定しなくて済むよう、YAML で設定できます。

```sh
mkdir -p ~/.config/sokki
cp sokki.example.yaml ~/.config/sokki/config.yaml
./bin/sokki list                           # 正確なデバイス名を確認
$EDITOR ~/.config/sokki/config.yaml        # capture.device を書き換える
```

`~/.config/sokki/config.yaml` があれば**自動で読み込みます**（無ければ従来どおり既定値で動作）。
自動で読む場所はこの 1 つだけです。他の場所に置いた設定は `--config` で明示してください。

別の場所に置いた設定を使う場合:

```sh
./bin/sokki --config /path/to/config.yaml
```

### 設定できる項目

```yaml
capture:
  type: device                 # 現在は "device" のみ
  device: "Conference Output"  # `sokki list` で確認した名前
transcription:
  language: ja                 # ja / en / auto
  model: ""                    # 空なら既定の探索順
output:
  directory: transcripts
```

全項目の説明は同梱の [`sokki.example.yaml`](sokki.example.yaml) を参照してください。

### 優先順位

**明示した CLI フラグ > 設定ファイル > 既定値**

```sh
# 設定ファイルに device: "BlackHole 2ch" と書いてあっても、
# この実行では Loopback 側を使う
./bin/sokki --device "Conference Output"
```

指定しなかった項目だけが設定ファイルの値を使います（項目ごとに独立して判定します）。

### 設定ファイルのエラー

誤りは**黙って無視せず停止**します。ファイル名・行番号・原因を表示します。

- 未知のキー（打ち間違い）
- 重複したキー
- 値の型の誤り（`device: 123` のような数値・真偽値。名前として使うなら `"123"` と引用符で囲む）
- 未対応の `capture.type`
- **空欄**（`device: ""` や `device:`）— 既定デバイスへ黙って戻さず停止します
- 複数の YAML 文書（`---` 区切り）— 2 文書目以降が無視されるのを防ぐため

> `transcription.model` だけは空欄 (`""`) を許可します（「既定の探索順を使う」意味）。

```
エラー: 設定ファイルの内容が不正です (/Users/you/.config/sokki/config.yaml):
  yaml: unmarshal errors:
    line 3: field devicce not found in type config.CaptureSection
対処: sokki.example.yaml を参考に、キー名・字下げ・値の型を確認してください
```

## BlackHole 2ch を使う（無料）

Loopback を持っていない場合、無料の [BlackHole](https://github.com/ExistentialAudio/BlackHole) が使えます。
会議音声を BlackHole と自分のヘッドホンの**両方**へ流すために、
macOS の **Multi-Output Device** を作成します。

### 1. BlackHole 2ch をインストール

```sh
brew install blackhole-2ch
```

インストール後、`./bin/sokki list` に `BlackHole 2ch` が出れば成功です
（出ない場合は再起動が必要なことがあります）。

### 2. Multi-Output Device を作る

これを作らないと、会議音声が BlackHole にだけ流れて**自分に聞こえなくなります**。

1. **Audio MIDI 設定**（`/Applications/Utilities/Audio MIDI Setup.app`）を開く
2. 左下の **＋** → **「複数出力装置を作成」**
3. 右の一覧で **自分のヘッドホン／スピーカー** と **BlackHole 2ch** の両方にチェック
4. **自分のヘッドホン／スピーカーを「主装置」** にする
5. BlackHole 2ch の **「ドリフト補正」にチェック**を入れる
6. 分かりやすい名前（例: `会議 + 録音`）に変更する

> 手順の詳細は
> [BlackHole 公式の Multi-Output Device 手順](https://github.com/ExistentialAudio/BlackHole/wiki/Getting-Started%3A-Creating-a-Multi-Output-Device)
> を参照してください。

### 3. 会議アプリの出力先を変える

会議アプリ（Teams / Zoom 等）の**スピーカー設定**を、作成した
Multi-Output Device（例: `会議 + 録音`）に変更します。

> `sokki` は音声の出力先を自動で変更しません。この設定はご自身で行ってください。

### 4. sokki の入力を BlackHole にする

```yaml
capture:
  device: "BlackHole 2ch"
```

または `./bin/sokki --device "BlackHole 2ch"`

### 音が聞こえない場合の確認

| 症状 | 確認すること |
|---|---|
| 自分に会議音声が聞こえない | Multi-Output Device に**ヘッドホンも**含まれているか。会議アプリの出力先が Multi-Output Device になっているか |
| 文字起こしが始まらない（無音のまま）| 会議アプリの出力先が Multi-Output Device か。`capture.device` が `BlackHole 2ch` か |
| 音が途切れる・ずれる | Multi-Output Device で BlackHole 2ch の**ドリフト補正**が有効か。主装置が自分のヘッドホンか |
| `BlackHole 2ch` が一覧に出ない | インストール後に Mac を再起動。`sokki list` で名前を再確認 |

## モデルを持っていない場合

Superwhisper などを使っておらず手元に ggml モデルが無い場合、無料で入手できます。
**自動ダウンロードは行いません**（どこから何を取得するかを明示するためです）。

モデルは [Hugging Face の whisper.cpp 公式リポジトリ](https://huggingface.co/ggerganov/whisper.cpp)
から入手できます。

| モデル | ファイル名 | サイズの目安 | 用途 |
|---|---|---|---|
| large-v3-turbo | `ggml-large-v3-turbo.bin` | 約 1.6 GB | **推奨**。精度と速度のバランスが良い |
| medium | `ggml-medium.bin` | 約 1.5 GB | large が重い場合 |
| small | `ggml-small.bin` | 約 0.5 GB | 省メモリ。日本語の精度は落ちる |
| base | `ggml-base.bin` | 約 0.15 GB | 動作確認用 |

取得して任意の場所に置いたら、設定ファイルで絶対パスを指定します。

```yaml
transcription:
  model: "/Users/you/models/ggml-large-v3-turbo.bin"
```

または `./bin/sokki --model /Users/you/models/ggml-large-v3-turbo.bin`

> 既定の探索先（「必要なもの」節）に置けば、設定なしでも読み込まれます。

## 日本語と英語が混ざる会議

`language` に `auto` を指定すると、**区間ごとに**言語を自動判定します。

```yaml
transcription:
  language: auto
```

または `./bin/sokki --lang auto`

```
[00:00:01] 本日の会議を始めます
[00:00:04] Let's review the release plan.
```

### 制限（重要）

言語判定は **VAD が区切った確定区間ごと**に行われます。
**同一区間の中で言語が頻繁に切り替わる場合の正しい出力は保証しません。**

間を置かずに「日本語 → 英語」と話すと 1 つの区間にまとまり、
**先に判定された言語だけが出力され、もう一方が欠落することがあります**
（合成音声での実測で確認済み）。これは whisper が 1 回の推論単位につき
1 言語を選ぶ仕様によるもので、本ツール側では回避していません。

**暫定表示との組み合わせ**: 暫定行は発話開始から約 1 秒の短い音声を認識するため、
`auto` では言語の判定が安定せず、**暫定文が実際と違う言語で出ることがあります**。
確定時に区間全体をもう一度認識するので、最終的な保存内容はそれより安定します。
表示途中の文は誤り得るものとしてお読みください。

安定させたい場合:

- 言語を切り替えるときに**一拍置く**（無音が入れば別区間になります）
- 使う言語が決まっているなら `ja` / `en` を**明示する**（`auto` より安定します）
- 暫定の誤りが気になるなら `--interim-interval` を長くするか `--interim=false`

## 入力デバイスを変える

```sh
./bin/sokki list                           # 一覧を表示
./bin/sokki --device "Jabra Elite 10"      # その実行だけ変える
```

`sokki list` は設定ファイルに貼れる形で名前を表示します。

```
音声入力デバイス一覧 (capture.device に指定できる名前):

  "Conference Output"
  "BlackHole 2ch"
  "MacBook Proのマイク"

設定ファイル (~/.config/sokki/config.yaml) に書く場合:

  capture:
    device: "Conference Output"

その実行だけ指定する場合:

  sokki --device "Conference Output"
```

**設定ファイルが壊れていても `sokki list` は動きます**（設定もモデルも読まないため）。
デバイス名を確認してから設定を直せます。

恒久的に変えるなら設定ファイルに書きます。

```yaml
capture:
  device: "社内会議キャプチャ"    # 名前を変えた Loopback デバイスでも可
```

既定は `Conference Output` です。指定したデバイスが見つからない場合は
**システム既定のマイクに切り替えず、エラーで停止**し、検出したデバイス一覧を表示します
（自分の声の混入を防ぐため）。

## モデル・言語を変える

```sh
./bin/sokki --model ~/models/ggml-large-v3.bin
./bin/sokki --lang en
./bin/sokki -l ja                               # --lang の短縮形
./bin/sokki --lang auto                           # 日英混在の会議
./bin/sokki --prompt "KDDI, Red Hat, OpenShift"   # 固有名詞のヒント
```

設定ファイルなら:

```yaml
transcription:
  language: auto
  model: "/Users/you/models/ggml-large-v3-turbo.bin"
```

`--prompt` は認識させたい固有名詞を渡すと表記が安定します。

## 主なオプション

| オプション | 既定値 | 説明 |
|---|---|---|
| `--config` | `~/.config/sokki/config.yaml` | 設定ファイルのパス |
| `--device` | `Conference Output` | 入力デバイス名 |
| `--model` | 自動探索 | ggml モデルのパス |
| `--lang`, `-l` | `ja` | 認識する言語（`ja` / `en` / `auto`）|
| `--out` | `transcripts` | 保存先ディレクトリ |
| `--interim` | `true` | 暫定結果を表示する |
| `--silence` | `500ms` | この長さの無音で発話を確定 |
| `--interim-after` | `1s` | 発話開始から最初の暫定表示までの時間 |
| `--interim-interval` | `1s` | 暫定表示を更新する間隔 |
| `--max-segment` | `12s` | 1 区間の最大長（話し続けてもここで区切る）|
| `--vad-threshold` | `0.004` | 発話判定の下限しきい値 |
| `--threads` | CPU 数の半分 | 推論スレッド数 |
| `--server-url` | — | 起動済み `whisper-server` を使う |
| `--list-devices` | — | デバイス一覧を表示して終了（`sokki list` と同じ）|
| `--pane-role` | — | AI 連携時の panecom role 名（指定すると会議ボードを表示）|
| `--session-id` | — | 会議 ID（省略時は開始時刻から生成）|
| `--mux-session` | — | Zellij / tmux のセッション識別子（省略時は環境から判定）|
| `--board` | `true` | pane 内に会議ボードを表示する |

### 調整の目安

- **細かい発話が拾われない** → `--vad-threshold` を下げる（例 `0.003`）
- **エアコン等の雑音を拾う** → `--vad-threshold` を上げる（例 `0.012`）
- **1 行が長すぎる** → `--max-segment 8s`
- **語尾が切れる** → `--silence 1s`
- **表示をもっと速くしたい** → `--interim-after 500ms --interim-interval 500ms`
  （詳しくは「[リアルタイム表示の挙動](#リアルタイム表示の挙動)」）

入力プロセスが生きたまま音声サンプルを 10 秒間返さなくなった場合、
`sokki` は途中の発言を確定してから取り込みを再接続します。
画面に再接続の警告を出し、その間の音声は記録できません。

## リアルタイム表示の挙動

話している間、**同じ 1 行が書き換わっていき**、発話が終わると確定行になります。

```
… [00:00:03] こんにち                    ← 発話開始から約 1 秒
… [00:00:03] こんにちは、日本の皆さん      ← 同じ行が更新される
[00:00:03] こんにちは、日本の皆さん        ← 発話区間を確定・保存
```

暫定行と確定行の時刻は発話区間の開始時刻です。
暫定文は文字を足していくのではなく、**発話開始からの音声を毎回認識し直して
全体を差し替えます**。そのため途中で**訂正されたり短くなったりすることがあります**。

| タイミング | 動作 |
|---|---|
| 発話開始から約 **1 秒** | 最初の暫定行を表示 |
| 以後約 **1 秒**ごと | 発話開始からの音声を**まるごと再認識**し、暫定行を書き直す |
| 無音 約 **0.5 秒** | 発話区間を確定・保存 |
| 発話が **12 秒**続く | 発話区間を区切って確定・保存 |

- 暫定行は**常に 1 行**で、端末幅に収まるよう直近の発話側を残して表示します
- 暫定行は**ファイルに保存しません**。確定時は音声全体に加えて、音の小さい境目で約 5 秒ずつに分けた区間も認識します。両方の結果を照合して省かれた発話を補います。分割した音声は重複しません
- 出力をリダイレクトした場合（非 TTY）は、暫定行を出さず確定行だけを出します
- 推論が表示間隔に追いつかないときは、**古い暫定を捨てて最新を優先**します。
  確定結果の保存を優先します。処理が長時間滞って保存できなかった区間があれば、
  終了時に件数を警告します

### 確定済みの行は書き直しません

一度確定してファイルに書いた行は、後から認識し直して修正することはしません。
12 秒の上限で区切られた行も同様です。

### 速さと正確さの調整

```sh
./bin/sokki --interim-after 500ms --interim-interval 500ms   # もっと速く出す
./bin/sokki --silence 1s                                     # 語尾の切れを防ぐ
./bin/sokki --interim=false                                  # 暫定表示を止める
```

- **表示が遅い / 反応が鈍い** → `--interim-after` と `--interim-interval` を短く
- **暫定の誤りが気になる** → `--interim-interval` を長く、または `--interim=false`
- **文が途中で切れる** → `--silence` を長く（例 `800ms`）
- **確定が遅い** → `--silence` を短く（例 `300ms`）

（`--vad-threshold` や `--max-segment` など他の調整は
「[調整の目安](#調整の目安)」を参照）

> 間隔を短くするほど CPU / GPU の負荷は上がります。暫定の更新ごとに
> 発話開始からの音声をすべて認識し直すためです。

## AI と連携する（MCP）

会議中に AI へ「今何を話している？」「決まったことと宿題は？」と聞くと、
AI が文字起こしを読んで答え、**会議ボード**を更新します。
会議ボードは pane の下側に表示され、同時に Markdown に保存されます。
発言ログも会議ボードも、会議中にその場でスクロールして読み返せます。

```
 ▶ 発言ログ 120–135 / 412 行 追従中 ──── Tab 切替 j/k 行 PgUp/PgDn 頁 g/G 先頭末尾
 [00:12:10] 来週のリリースについて…
 … [00:12:18] 担当は山田さんで、                    ← 暫定行はログの末尾で更新
   会議ボード（自動更新） 版 7 1–9 / 34 行 追従中 ──────────────────────────────
 今の話題: 来週のリリース
 要約:
 ・テストを金曜までに終える [00:11:42]
 ・手順の確認は山田さん [00:12:10]
```

画面はおよそ **上 6 : 下 4**（発言ログ : 会議ボード）で、どちらも
スクロールして読み返せます。会議ボードの「今の話題」は固定行にも表示され、
長い要約の末尾を見ている間も確認できます。

| キー | 動き |
|---|---|
| `Tab` | 操作する領域を切り替える（見出しの `▶` が現在の対象）|
| `j` / `k`、`↓` / `↑` | 1 行ずつ移動 |
| `PageDown` / `PageUp`、`Ctrl+d` / `Ctrl+u` | ページ / 半ページ移動 |
| `g` / `G` | 先頭へ / 末尾へ（`G` は最新への追従も再開）|

上へスクロールした領域は**最新への追従を止め**、見出しに `未読 N 行` を出します。
もう一方の領域は更新を続け、録音・保存・自動要約も止まりません。
新しい発言や要約の更新で**読んでいる位置が勝手に飛ぶことはありません**。

### 前提

- **Zellij または tmux のセッション内**で、`panecom` が使えること
- AI クライアント（Claude Code / Codex 等）が MCP に対応していること

> panecom や多重化セッションが使えない場合、**音声デバイスを開かずにエラー**を返します
> （画面の無い録音を意図せず始めないため）。
> MCP クライアントが Zellij / tmux の環境変数を落とす場合は、MCP サーバーが
> 親プロセスからセッション名と pane ID を復元します。復元できない場合でも、
> **Zellij 自体が使えないとは限りません**。呼び出し元のシェルで
> `panecom whoami` を確認してください。画面のない直接録音には切り替えません。
> MCP と同じ経路の読み取り専用チェックは `./bin/sokki mcp --check-panecom` で行えます。
>
> 開いた pane の role を解決できない場合（AI の pane と panecom の namespace が
> 異なる場合）も、**録音を開始せずに**原因を返します。その際は
> `--panecom-dir`（または環境変数 `SOKKI_PANECOM_DIR`）に AI pane と同じ
> 作業ディレクトリを指定してください。

### AI クライアントへの登録

```json
{
  "mcpServers": {
    "sokki": {
      "command": "/absolute/path/to/bin/sokki",
      "args": ["mcp"]
    }
  }
}
```

`sokki mcp` 自体は**音声デバイスを開きません**。録音は専用 pane の別プロセスで動きます。

### 使い方

1. AI に「会議の文字起こしを始めて」と頼む（AI が `start_or_attach` を呼ぶ）
2. 新しい会議ごとに専用 pane が開き、録音が始まる。**フォーカスは AI の pane に残る**
3. 上側で発言ログ、下側で会議ボードを見る
4. 聞き逃したら AI に質問する。AI が答え、会議ボードを更新する
5. AI に「録音を止めて」と頼むか、録音 pane で `Ctrl-C`

### 自動要約

MCP の `start_or_attach` と手動 CLI で始めた会議は、**質問しなくても**会議ボードが
自動で作られます。不要な場合は `--auto-summary=false` または設定ファイルの
`summary.enabled: false` で無効にできます。

| 設定 | 既定 | 意味 |
|---|---|---|
| `--auto-summary` | 有効 | 発言から会議ボードを自動更新 |
| `--summary-provider` | `claude` | 要約に使う AI（プリセットは現在 `claude` のみ）|
| `--summary-model` | CLI の既定 | モデル |

更新のきっかけは「前回から **90 秒**以上経っていて、確定行が **8 行**以上たまった」とき。
未処理の発言があれば遅くとも **180 秒**で更新します。ただし最初の要約は、
予告や挨拶だけで作らないよう確定行を最低 **3 行**待ちます。
**発言が無い間は AI を呼びません。** 会議開始の直後にも呼びません。
AI は発言ごとに項目を増やすのではなく、同じ話題や繰り返しを統合して
会議ボード全体を書き直します。

設定ファイルでも指定できます。

```yaml
summary:
  enabled: true
  provider: claude            # プリセットは claude のみ
  model: ""                   # 省略時は CLI の既定
  command: []                 # 指定すると provider より優先（配列で渡す）
  min_new_lines: 8
  min_interval: 90s
  max_interval: 180s
  max_chars: 2000
```

> プロバイダーを起動できない場合（コマンドが無い、未対応の provider など）は、
> **自動要約だけを無効にして録音を続けます**。pane の見出しと `start_or_attach`
> の応答、`get_status` の `auto_summary` は実際の状態を返します。

> 要約のため、**確定した会議テキストが AI プロバイダーへ送られます**。
> 起動時の表示と `get_status` で有効かどうかを確認できます。
> 要約用の子プロセスにはツール実行・MCP・会話履歴を渡していません。
> `codex` はツールを無効にした実行形を確認できていないため、プリセットとしては選べません。
> 任意のコマンドを使う場合は設定ファイルの `summary.command` に配列で指定します。

失敗したときは反映位置を進めず、間隔を空けて再試行します。
文字起こしは影響を受けません。状態は `get_status` の `summary` で確認できます。

録音を止めると、確定行の保存を終えてから**最終要約**を 1 回行います。
`stop_session` は保存完了時点で応答し、最終要約の結果は `get_status` の
`summary.final_state` に出ます。

### 保存されるファイル

同じベース名で 3 つ作られます。

| ファイル | 内容 |
|---|---|
| `2026-10-16_150000.txt` | 文字起こし（確定行のみ） |
| `2026-10-16_150000.md` | **最新の会議ボード**（これが議事録） |
| `2026-10-16_150000.history.md` | 会議ボードの更新履歴（過去の版） |
| `.2026-10-16_150000.board.json` | 会議ボードの控え（終了後も版を引き継ぐため） |

### MCP ツール

| ツール | 動作 |
|---|---|
| `start_or_attach` | 録音中ならその会議を返す。無ければ pane を開いて起動（自動要約も開始）|
| `get_status` | 会議 ID・状態・開始時刻・pane role・保存先 |
| `read_transcript` | 指定位置以降の確定行（`next_from` で続きから） |
| `read_context` | 確定行 + 現在の暫定行 + 会議ボード |
| `publish_summary` | 会議ボードを手動で上書き（通常は自動更新に任せる）|
| `stop_session` | 通常終了を依頼し、確定行の保存を待つ（最終要約は `pending` で続行、結果は `get_status`）|

### 動作の約束

- **AI が接続を閉じても録音は続きます**（録音の寿命は AI の接続から独立）
- **会議ボードは発言から自動で作成・更新されます**（MCP 起点の録音は既定で有効）。
  質問しなくても、確定した発言が 8 行以上たまり前回から 90 秒経つと更新されます
- **二重起動しません**。録音中に `start_or_attach` を呼ぶと既存の会議に接続します
- **会議の内容を pane のシェルに注入しません**。要約の受け渡しはローカル IPC（Unix
  domain socket）で行います
- 自動要約が有効な場合は、停止時に未処理の確定行を対象に最終要約を 1 回行います
- 最後の要約以降に話した内容は、**録音終了後でも** AI に頼んで Markdown へ反映できます。
  終了後の `publish_summary` は版を引き継いで `.md` と履歴を更新します
  （画面には反映できないため、応答の `screen_updated` が `false` になります）
- 画面反映と Markdown 保存は**同じ操作**です。最新 `.md`・履歴・控えのすべてが
  書けたときだけ版が進みます。保存先が書けない場合（権限、ディレクトリで塞がれている、
  `chflags uchg` による変更不可、通常ファイル以外など）は**何も書き換えずに失敗**を
  返すので、「公開は失敗したのにファイルだけ新しい」状態になりません。
  置き換えの途中で失敗した場合も、それまでに書いた分を元の内容へ戻します
- `stop_session` は**未処理の確定行が保存され、ファイルを閉じるまで待って**から返します。
  直後に `read_transcript` しても最後の発言を取りこぼしません
- **別の Zellij / tmux セッションの録音には接続しません**。会議 ID を省略した操作
  （接続・状態確認・停止）は、今いるセッション内の録音だけを対象にします。
  セッションを特定できない場合（Zellij / tmux の外）は、会議を自動選択せず
  エラーを返します。会議 ID を明示した場合も、**録音中の会議**なら所属を検証します
  （終了済みの会議は別セッションからでも読み書きできます）
- 起動前（**設定ファイルのエラー**・モデル不在・デバイス不在・whisper 起動失敗など）に
  終了した場合も、**原因が `failed` 状態として記録**され `get_status` で読めます
- `start_or_attach` は**実際に録音が始まる（`recording`）まで待ってから**成功を返します。
  準備中（`starting` / `loading`）のまま時間切れになった場合は、その状態を添えて
  エラーを返します（成功と報告したあとに初期化が失敗することはありません）

### 手動実行との関係

`--pane-role` も `--session-id` も付けずに起動した場合、MCP からの操作はできません。
自動要約が有効なら、TTY では発言ログと会議ボードを上下に表示します。
`--auto-summary=false` の手動実行と非 TTY（リダイレクト）では、従来どおりの行出力になります。

## 動作の仕組み

```
設定した入力デバイス (Loopback / BlackHole 2ch など, 48 kHz / stereo)
  → ffmpeg (avfoundation, -ac 1 で Ch1&2 を統合, 16 kHz s16le)
  → 無音区間検出 (VAD) で発話区間を作る
  → whisper-server (127.0.0.1, ggml-large-v3-turbo) で全体・分割の両方を認識
  → 重複・無音由来の定型句を除去 → ターミナル表示 + ファイル追記
```

直前の区間と時間が近く、内容も重なる場合は重複を抑制します。
無音区間で whisper が出しやすい定型句（「ご視聴ありがとうございました」等）も除外します。

## エラーが出たら

| メッセージ | 対処 |
|---|---|
| `入力デバイス '...' が見つかりません` | 表示された一覧から正確な名前を `capture.device` か `--device` に指定（名前は `sokki list` で確認）。仮想デバイス（Loopback / BlackHole 2ch）が有効かも確認 |
| `モデルが見つかりません` | `transcription.model` か `--model` で ggml 形式の `.bin` を指定（→「モデルを持っていない場合」）|
| `設定ファイルの内容が不正です` | 表示された行番号とキー名を確認（→「設定ファイルのエラー」）|
| `設定ファイルが見つかりません` | `--config` のパスを確認。既定値で動かすなら `--config` を外す |
| `capture.type: "app" は未対応です` | 現在は `device` のみ。Loopback / BlackHole の仮想デバイス名を `capture.device` に指定 |
| `panecom を利用できません` | `./bin/sokki mcp --check-panecom` と呼び出し元シェルの `panecom whoami` で接続先を確認（→「AI と連携する（MCP）」）|
| `録音中の会議がありません` | AI に `start_or_attach` を頼む |
| `録音プロセスに接続できません` | 録音 pane が終了している。`get_status` で確認し、必要なら再開 |
| `開いた pane の role ... を解決できません` | `--panecom-dir` に AI pane と同じ作業ディレクトリを指定（録音は開始していません）|
| `会議ボードの履歴を開けません` | 保存先の権限を確認。古い版を失わないため公開していません |
| `保存の完了を確認できませんでした` | `get_status` で録音プロセスの状態を確認 |
| `音声入力デバイスを 1 つも検出できませんでした` | システム設定 → プライバシーとセキュリティ → マイク で、実行元のターミナルアプリを許可 |
| **会議音声が聞こえなくなった** | Loopback の「Mute when capturing」を無効化（冒頭の設定手順を参照）|
| `whisper-server がリダイレクトを返しました` | 音声を外部へ送らないため転送を拒否した。`--server-url` の指定先を確認 |
| `whisper-server が起動しませんでした` | `brew install whisper-cpp`、モデルが ggml 形式か確認 |
| `設定ファイルに複数の YAML 文書があります` | 文書区切り `---` を削除し、設定を 1 つの文書にまとめる |
| `capture.device が空です` | 値を入れるか、その行ごと削除して既定値を使う（空欄は既定デバイスへ戻さず停止します）|
| `文字列で指定してください` | 数値や真偽値に見える名前は引用符で囲む（例: `device: "123"`）|
| 強制終了後も会議音声が戻らない | 下記「強制終了した場合」を参照 |

### 強制終了した場合

`sokki` は `ffmpeg` と `whisper-server` を**独立した process group** で起動します
（端末の `Ctrl-C` が子プロセスを直接止めてしまい、最後の発話を保存できなくなるため）。

`Ctrl-C` での通常終了では子プロセスも確実に停止します。ただし `kill -9` などで
**強制終了した場合は子プロセスが残り**、`ffmpeg` が入力デバイスを掴んだままになります
（Loopback の "Mute when capturing" が有効だと会議音声が聞こえないままになります）。

残っていないかの確認と後始末:

```sh
pgrep -fl 'ffmpeg.*avfoundation|whisper-server'   # 残留確認
pkill -f 'ffmpeg.*avfoundation'; pkill -f whisper-server   # 後始末
```

## テスト

```sh
make test         # 単体テスト（音声デバイス不要）
make test-e2e     # 実機テスト（whisper-server + 実モデルを使用）
```

`make test-e2e` は `say` で生成した日本語音声を使い、
区間検出 → whisper.cpp → 重複抑制 → 保存 の全段を通します（音声デバイスは使いません）。
