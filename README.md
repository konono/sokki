# sokki — 会議音声のローカル文字起こし

仮想オーディオデバイス（Loopback / BlackHole）に流れる会議音声を、このMac上で
文字起こしします。既定の音声認識はwhisper.cppです。Qwen3-ASR も明示選択できます。結果はターミナルに逐次表示し、会議ごとにテキストファイルへ保存します。

- 既定の推論はローカルの **whisper.cpp** (`whisper-server`) を外部プロセスとして使用します。Qwen 選択時はローカル Python worker を使います
- whisper の音声認識は `127.0.0.1` の whisper-server で処理します。**既定で有効な自動要約では、確定した会議テキストが設定した AI プロバイダーへ送られます**
- 入力は設定したデバイスのみです（既定のマイクには切り替えません）。自分の声がそのデバイスに流れていれば、自分の声も文字起こしの対象になります
- **音声ファイルは保存しません**（テキストのみ）
- 入力デバイス・モデル・言語・保存先は **YAML 設定ファイル**で変更できます

**Loopback を持っていない場合**は、無料の **BlackHole 2ch** が使えます
（→「[BlackHole 2ch を使う（無料）](#blackhole-2ch-を使う無料)」）。

## できることと、選ぶ構成

**音声認識を1つ、要約・翻訳で共通に使うLLMを1つ選び、必要なものだけ用意してください。**
既定は **Whisper + Claude要約** です。要約なしなら `--auto-summary=false` を明示します。翻訳は `--translate en-ja` で有効にします。

| やりたいこと | 選択・設定 | 追加で必要なもの | 準備手順 |
|---|---|---|---|
| 会議を文字起こしして `.txt` 保存 | `--backend whisper`（既定） | whisper.cpp の `whisper-server` + GGML音声認識モデル | [Whisper](#setup-whisper) |
| Qwenで日本語を文字起こし | `--backend qwen --final-strategy whole` | Apple Silicon、Python 3.12隔離環境、MLX系依存、Qwen3-ASR BF16モデル | [Qwen](#setup-qwen) |
| 文字起こしだけ使う | `--auto-summary=false --translate ""` | LLM用ツール不要 | 上の音声認識をどちらか準備 |
| Claudeで会議ボード・議事録を自動更新 | `--llm-provider claude`（既定） | Claude Code CLI、利用可能な認証・契約、ネット接続 | [Claude](#setup-claude) |
| ローカルLLMで自動要約を試す | `--llm-provider ollama --llm-model MODEL` | 起動済みOllama、取得済みローカルLLM | [Ollama](#setup-ollama) |
| 英語会議を日本語にライブ翻訳 | `--backend whisper --lang en --translate en-ja` | Whisper一式と、共通設定で選ぶClaudeまたはOllama | [翻訳](#setup-translation) |
| AIに録音開始・停止や議事録参照を頼む | `sokki mcp` | MCP対応クライアント、panecom、Zellijまたはtmux | [MCP](#setup-mcp) |

全録音構成で **macOS・ffmpeg・入力デバイス・マイク権限** が必要です。
会議アプリの音を取る場合はLoopbackまたはBlackHoleを設定します。
手動CLIでの録音・自動要約にはMCP、panecom、Zellij/tmuxは不要です。
Qwen選択時はwhisper.cpp不要、Ollama要約にはQwen用Python不要です。

**Qwen3-ASR（音声→文字）と、OllamaのQwen系LLM（文字→要約）は別モデルです。**
モデルファイルや実行環境を流用することはできません。
ローカル要約は選択可能ですが、現時点ではClaudeを既定にしています。
要約・翻訳は同じ `--llm-provider` / `--llm-model` を使います。それぞれの有効・無効は独立です。
翻訳は英語Whisper→日本語表示に対応し、英語原文も保持します。

### 最初の準備の順序

1. [共通依存・ビルド](#setup-common)を準備する。
2. [音声ルーティング](#audio-setup)を設定し、`./bin/sokki list` で入力を確認する。
3. WhisperまたはQwenの手順で、**実行環境とモデルの両方**を準備する。
4. 要約または翻訳を使うならClaudeまたはOllamaの手順を済ませる。両方不要なら無効にする。
5. [会議前チェック](#preflight)を通してから本番の会議で使う。

**Qwen/Ollama要約は `1bef131` 以降、ライブ翻訳と共通LLM設定は `71acba2` 以降を含むソース向けです。**
現時点ではローカル実験ブランチに実装済みで、公開 `main` へは未反映です。
新規に公開リポジトリをcloneするだけでは、これらの機能はまだ入りません。
今は新機能を含む既存チェックアウトからビルドしてください。

以下のコマンドはそのリポジトリ直下で実行します。`./bin/sokki` は `make build` で作る
通常のバイナリです。個別環境向けの `sokki-qwen` ランチャーは新規導入に必須ではありません。

<a id="audio-setup"></a>

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

<a id="setup-common"></a>

## 共通依存とビルド

対象はmacOSです。Qwen経路は **Apple Silicon（`uname -m` が `arm64`）** 向けです。
依存の導入には以下で[Homebrew](https://brew.sh/)を使います。
Homebrew未導入なら先に公式手順で導入し、案内されるPATH設定を済ませてください。

| 必要なもの | いつ必要か | 事前確認 |
|---|---|---|
| Xcode Command Line Tools（Git・make等） | ソース取得・ビルド、Qwen依存のGit取得 | `xcode-select -p` / `git --version` / `make --version` |
| Go 1.26以上 | sokkiのビルド時 | `go version` |
| ffmpeg（AVFoundation対応） | 全録音構成の実行時 | `ffmpeg -hide_banner -devices` に `avfoundation` がある |
| 入力デバイスとマイク権限 | 全録音構成の実行時 | `./bin/sokki list` に使いたいデバイスがある |
| 保存先の空き容量・書込権限 | テキスト・議事録の保存 | `output.directory` / `--out` を確認 |

未導入のものを会議前にまとめて準備します。

```sh
xcode-select --install          # Command Line Toolsが未導入の場合。インストール完了を待つ
brew install go ffmpeg

# 新機能を含むsokkiのチェックアウトへ移動してから実行
make build                     # bin/sokki を作る。Go依存はgo.mod/go.sumに従って取得
./bin/sokki --help
./bin/sokki list
```

システム設定 → プライバシーとセキュリティ → マイクで、実行元のターミナルアプリを許可します。
MCPから使う場合も、実際に録音paneを開くターミナルで許可が必要です。
モデルの取得・パッケージ導入にはネット接続と空き容量が必要です。
sokkiは実行時に依存ツール・モデルを自動インストールしません。

<a id="setup-whisper"></a>

## Whisperで文字起こしする準備

**必要なものは `whisper-server` とGGMLモデルの2つです。**
Python版 `openai-whisper` や、CLIだけを置いても代用できません。
Homebrewの現在のformula名は `whisper.cpp`（旧名 `whisper-cpp`）です。
モデルはパッケージに含まれません。[Homebrew公式情報](https://formulae.brew.sh/formula/whisper.cpp)

```sh
brew install whisper.cpp
command -v whisper-server
whisper-server --help

# 固定revisionのモデルを取得し、完全性を確認してから正式ファイル名へ配置
(
  set -e
  mkdir -p "$HOME/models"
  cd "$HOME/models"
  curl -fL --retry 3 \
    -o ggml-large-v3-turbo.bin.download \
    https://huggingface.co/ggerganov/whisper.cpp/resolve/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo.bin
  printf '%s\n' '1fc70f774d38eb169993ac391eea357ef47c88757ef72ee5943879b7e8e2bc69  ggml-large-v3-turbo.bin.download' | shasum -a 256 -c -
  mv ggml-large-v3-turbo.bin.download ggml-large-v3-turbo.bin
)
```

`ggml-large-v3-turbo.bin.download: OK` と出て正常終了してから、次の起動へ進みます。
失敗した `.download` はモデルとして使わず、原因を解消して取得をやり直してください。
サイズ・SHAは[配布元のモデル情報](https://huggingface.co/ggerganov/whisper.cpp/blob/5359861c739e955e79d9a303bcbc70fb988958b1/ggml-large-v3-turbo.bin)に対応します。
large-v3-turboのモデルは約1.6GBです。既存のGGMLモデルがある場合は再取得不要です。
対応する別モデルは[モデル一覧](#モデルを持っていない場合)を参照してください。

```sh
# 文字起こしのみ。要約ツールは不要
./bin/sokki --backend whisper --final-strategy hybrid \
  --model "$HOME/models/ggml-large-v3-turbo.bin" --auto-summary=false --translate ""
```

入力が既定の `Conference Output` と違う場合は `--device "BlackHole 2ch"` などを加えます。
通常はsokkiが `whisper-server` を起動・終了するため、手でサーバーを常駐させる必要はありません。

モデル未指定時は次の順で探索します。

1. `~/Library/Application Support/superwhisper/ggml-large-v3-turbo.bin`
2. `~/.local/share/talknote/models/ggml-large-v3-turbo.bin`

Superwhisperの `models/` 配下にあるCoreML形式は対象外です。
上記以外に置いた `.bin` は `transcription.model` または `--model` で指定します。

## 使い方

初回は、上のWhisperまたは下のQwenの**モデルパス付き起動例**を使います。
入力・モデル・要約の設定を[設定ファイル](#設定ファイル)に保存した後は、次で起動できます。

```sh
./bin/sokki list   # 入力デバイス名を確認
make run           # 保存した設定で起動。または ./bin/sokki
```

設定なしでは `Conference Output`、Whisperの既定モデル探索、Claude要約が選ばれます。
`$HOME/models` に取得しただけのモデルは既定探索対象ではないため、起動例の `--model` またはYAML指定が必要です。

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
  既定のhybridでは全体と短い区間の両方で認識するため、暫定文と内容が異なる場合があります

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

<a id="setup-qwen"></a>

## Qwen3-ASRで文字起こしする準備

Apple SiliconとPython 3.12の隔離環境を使います。**OllamaでQwenを取得しても、この音声認識には使えません。**
以下は別々に実行しても同じパスを使う例です。依存とモデルの取得を会議前に完了させます。

```sh
brew install python@3.12 git
python3.12 -m venv "$HOME/.venvs/sokki-qwen"
"$HOME/.venvs/sokki-qwen/bin/python" -m pip install -r requirements-qwen.txt
"$HOME/.venvs/sokki-qwen/bin/hf" download mlx-community/Qwen3-ASR-1.7B-bf16 \
  --revision e1f6c266914abc5a46e8756e02580f834a6cf8a7 \
  --local-dir "$HOME/models/Qwen3-ASR-1.7B-bf16"
```

[`requirements-qwen.txt`](requirements-qwen.txt)が `mlx-audio` の固定commit、`mlx` / `mlx-metal`、
NumPy、Hugging Face Hubを導入します。`hf` はこの環境に入るモデル取得用CLIです。
音声認識モデルの重みは約4.1GBで、Python環境・キャッシュ・実行時メモリも別途必要です。
モデルはディレクトリ全体を保持してください（`config.json`、`model.safetensors`、tokenizer等）。
任意の量子化版や別モデルへの互換性は保証していません。
[Python導入](https://formulae.brew.sh/formula/python@3.12) / [hf download](https://huggingface.co/docs/huggingface_hub/guides/cli)

### 録音を始めずに準備を確認する

```sh
"$HOME/.venvs/sokki-qwen/bin/python" -m pip check
"$HOME/.venvs/sokki-qwen/bin/python" -c 'import mlx.core, numpy; from mlx_audio.stt import load; print("imports OK")'

# 実製品と同じworkerでモデルをロードし、合成無音を認識して終了する（マイクは開かない）
HF_HUB_OFFLINE=1 TRANSFORMERS_OFFLINE=1 HF_DATASETS_OFFLINE=1 \
  "$HOME/.venvs/sokki-qwen/bin/python" internal/qwen/worker.py \
  "$HOME/models/Qwen3-ASR-1.7B-bf16" < /dev/null
```

最後のコマンドで `{"kind": "ready"}` が出て正常終了すれば、モデルロードと無音推論まで確認できています。
単に `pip install` が成功しただけより深い確認です。実行にはメモリと時間を使います。

```sh
./bin/sokki --backend qwen --final-strategy whole --lang ja --model "" \
  --qwen-python "$HOME/.venvs/sokki-qwen/bin/python" \
  --qwen-model "$HOME/models/Qwen3-ASR-1.7B-bf16" --auto-summary=false --translate ""
```

要約を使う場合は `--auto-summary=false` を `--auto-summary=true` に変え、[Claude](#setup-claude)または
[Ollama](#setup-ollama)の準備・設定を追加します。

- 現行Qwen経路は **`--lang ja` のみ**。`auto` / `en`、`--prompt`、`--server-url` は使えません。
- `--model ""` は既存YAMLのWhisperモデル指定を空にします。Qwen専用YAMLでも `transcription.model: ""` にします。
- Pythonとモデルは**絶対パス**を指定します。YAML内では `$HOME` や `~` を展開せず、`/Users/あなた/...` と書いてください。
- `--final-strategy hybrid` も選択可能です。`whole` は区間全体1回、`hybrid` は全体と分割・必要時retryを合流します。
- Whisperへ戻すときは `--backend whisper --final-strategy hybrid --model /absolute/path/to/ggml-model.bin` を指定します。
- 音声認識モデルのロードは録音開始前に行います。**要約LLMの背景ロードとは別**です。

<a id="setup-claude"></a>

## Claudeで自動要約する準備（既定）

**ClaudeのWeb/デスクトップアプリだけでなく、`claude` コマンドが必要です。**
sokkiを起動するユーザー・ターミナルから利用できる状態にします。
[Claude Code公式の導入・認証手順](https://code.claude.com/docs/en/quickstart)

```sh
brew install --cask claude-code
claude --version
claude                        # 初回の認証を済ませ、終了しておく
```

Claude Codeを利用できる契約・API認証などとネット接続が必要です。
インストールだけでは認証は完了しません。会議前に、sokkiが使う実行オプションでも確認します。
**次の確認は小さな生成リクエストを送り、利用枠・課金の対象になり得ます。** 会議内容は送りません。

```sh
printf '%s\n' 'JSONで {"topic":"接続確認","summary":"接続できました"} を返してください。' | \
  claude -p --output-format json --tools "" --strict-mcp-config --no-session-persistence
```

認証・利用上限・ネットワークエラーがなくJSONが返ることを確認します。
音声認識の起動例に `--auto-summary --summary-provider claude --summary-model ""` を追加すると使えます。
この構成では確定した文字起こしがClaudeへ送られます。認証失敗等では要約が失敗し、文字起こしは続きます。

<a id="setup-ollama"></a>

## Ollamaでローカル要約する準備（任意）

**Ollama本体・起動済みサーバー・取得済みモデルの3つを用意します。**
[公式ダウンロード](https://ollama.com/download/mac)からmacOSアプリを導入して起動します。
CLIで導入した場合は別ターミナルで `ollama serve` を起動しておきます。
アプリ等ですでに起動している場合は、サーバーを重複起動しません。
[公式CLI手順](https://docs.ollama.com/cli)

```sh
ollama --version
curl --fail --max-time 5 http://127.0.0.1:11434/api/version
ollama pull qwen3.5:9b         # 会議前に取得を完了。モデルだけで約6.6GB
ollama list                   # qwen3.5:9b が一覧にあること
```

製品の動作確認はOllama 0.33.3 / `qwen3.5:9b` で行いました。
比較した小型候補は `qwen3.5:9b`、`qwen3:8b`、`gemma4:e4b` です。
JSON schemaと `think:false` に対応するローカルモデルが必要で、全Ollamaモデルへの互換性は保証しません。
モデル容量とは別に実行時メモリを使い、音声認識モデルと同時に常駐します。
比較環境はM4 Max / 128GBであり、他のMacでの速度・メモリ余裕を保証するものではありません。
要約品質には課題が残るため、Claudeからの切替は明示した場合だけです。

会議前に、ダウンロード済みモデルをロードできるか確認できます（生成はしません）。

```sh
curl --fail --max-time 120 http://127.0.0.1:11434/api/chat \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.5:9b","messages":[],"stream":false,"think":false,"keep_alive":"5m","options":{"temperature":0,"seed":42,"num_ctx":16384,"num_predict":4096}}'
```

HTTP成功だけでなく、応答が `done: true`、`done_reason: "load"`、指定モデルであり、
`error` がないことを確認します。これはロード確認で、要約の内容品質の合格判定ではありません。

以下は上のQwen音声認識の準備も済ませた場合の起動例です。
Whisperを使う場合は[Whisperの起動例](#setup-whisper)の `--auto-summary=false` を外し、
同じ `--auto-summary` と要約用フラグを加えます。

```sh
./bin/sokki --backend qwen --final-strategy whole --lang ja --model "" \
  --qwen-python "$HOME/.venvs/sokki-qwen/bin/python" \
  --qwen-model "$HOME/models/Qwen3-ASR-1.7B-bf16" \
  --auto-summary --summary-provider ollama --summary-model qwen3.5:9b \
  --summary-ollama-url http://127.0.0.1:11434
```

- URLは **数値のloopback IP + HTTP + ポート**。`localhost`、HTTPS、認証付きURL、パス付きURLは現行実装で使えません。
- `summary.command` を設定している場合は削除してください。Ollamaとの併用はできません。
- sokkiはOllamaサーバーの起動や `pull` を行いません。**モデルの背景ロード**のみ行い、録音を待たせません。
- 準備失敗でも録音は続きます。初回要約までの準備完了を確かめたい場合は、会議前のロード確認を済ませます。
- 終了時に共有サーバーやモデルを強制停止しません。保持更新を止め、TTLでの解放に任せます。

要約の時刻・失敗時の挙動・出力ファイルは[自動要約](#自動要約)を参照してください。

<a id="preflight"></a>

## 会議前チェック

現時点では全依存を一括検査する `doctor` コマンドはありません。
**選んだ構成の行だけ、会議に使うターミナルから**確認してください。
インストール時と実行時のPATHが違うと、導入済みでも見つからないことがあります。

| 対象 | 会議前に確認すること | 完了の目安 |
|---|---|---|
| 共通 | `./bin/sokki --help`、`ffmpeg -hide_banner -devices`、`./bin/sokki list` | バイナリ起動、AVFoundation、入力名を確認 |
| 音声経路 | 会議アプリ→仮想デバイス、手元でのモニタ再生、マイク権限 | 自分にも会議音が聞こえる。`list`だけでは音声が流れることまでは確認できない |
| Whisper | `command -v whisper-server`、`whisper-server --help`、指定 `.bin` が取得済み | 実行ファイルとモデルの両方あり |
| Qwen | 上の `pip check`、import、worker無音起動 | `ready`を返して正常終了 |
| Claude要約 | `claude --version`、上のJSON生成確認 | 認証・利用枠を含めてリクエスト成功 |
| Ollama要約 | `/api/version`、`ollama list`、上のロード確認 | サーバー・モデル名・ロード成功を確認 |
| MCP | 同じpaneで `panecom whoami`、`./bin/sokki mcp --check-panecom` | 起動先セッション・paneが解決できる |
| 設定 | 自動読込YAMLの `backend` / `model` / `summary` / 保存先 | 今回使わないモデル名やコマンドが残っていない |

初回導入時は短い音声で録音→Ctrl-C→ `.txt` 保存まで一度確認してください。
要約なしで確認する場合は `--auto-summary=false` を付けます。
`sokki list` はモデルや要約設定を検査しないので、一覧が出るだけでは準備完了ではありません。

### 更新・切替時に揃えるもの

| 変更 | 一緒に確認する項目 |
|---|---|
| 新しい機能を使う | ソース更新後に `make build`。PATH上の古い `sokki` ではなく今回の `./bin/sokki` を使う |
| Whisper → Qwen | `backend`、`qwen_python`、`qwen_model`、`language: ja` を設定し、Whisper用 `model` を空にし、YAMLの `translation.mode` 行（または `translation` 節）を削除する。CLI切替では `--translate ""` を指定する |
| Qwen → Whisper | `backend: whisper`、GGML `model`、`final_strategy`。Qwen用パスは使われない |
| Claude → Ollama | 共通設定の `llm.provider: ollama`・取得済みモデルの `llm.model`・`llm.ollama_url` を設定し、`summary.command` を外す |
| Ollama → Claude | 共通設定の `llm.provider: claude` **と `llm.model: ""` の両方**を戻す |
| QwenのPython環境を更新 | 専用venv内でrequirementsを更新し、`pip check`とworker確認を再実行 |
| MCPから起動する | AI/MCPと録音paneの双方でPATH・YAML・絶対パスを確認 |

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
  backend: whisper              # whisper (既定) / qwen
  final_strategy: hybrid        # hybrid (既定) / whole
  language: ja                 # ja / en / auto
  model: ""                    # 空なら既定の探索順
  # Qwen 利用時のみ絶対パスで指定
  # qwen_python: "/Users/you/.venvs/sokki-qwen/bin/python"
  # qwen_model: "/Users/you/models/Qwen3-ASR-1.7B-bf16"
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

> モデル名など一部の任意項目は `""` を許可します。`transcription.model: ""` はWhisperでは既定探索、QwenではWhisper用指定なしの意味です。Ollama選択時の `summary.model` は必須です。

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

> [Whisperの既定探索先](#setup-whisper)に置けば、モデルパスの指定なしでも読み込まれます。

## 日本語と英語が混ざる会議

この節はWhisper経路向けです。現行Qwen経路は `ja` のみ対応します。

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

この節のモデルファイル・言語・prompt指定はWhisper経路向けです。

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

`--prompt` はWhisper経路だけの認識ヒントです。表記改善は保証せず、別の箇所が変化することもあります。

## 主なオプション

| オプション | 既定値 | 説明 |
|---|---|---|
| `--config` | `~/.config/sokki/config.yaml` | 設定ファイルのパス |
| `--device` | `Conference Output` | 入力デバイス名 |
| `--model` | 自動探索 | ggml モデルのパス |
| `--backend` | `whisper` | `whisper` / `qwen` |
| `--final-strategy` | `hybrid` | `hybrid` (全体+分割/C7) / `whole` (全体1回) |
| `--qwen-python` | — | Qwen 用の隔離 Python 実行ファイルの絶対パス |
| `--qwen-model` | — | ローカル Qwen3-ASR BF16 モデルディレクトリの絶対パス |
| `--lang`, `-l` | `ja` | 認識する言語（`ja` / `en` / `auto`）|
| `--translate` | 無効 | ライブ翻訳を明示選択 (`en-ja` のみ) |
| `--llm-provider` | `claude` | 要約と翻訳で共有する AI (`claude` / `ollama`) |
| `--llm-model` | 空 | 共通モデル。Ollama では必須 |
| `--llm-ollama-url` | `http://127.0.0.1:11434` | 共通の既存 Ollama server (数値loopbackのみ) |
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
- 暫定行は**ファイルに保存しません**。既定のhybridでは確定時に音声全体と、音の小さい境目で約 5 秒ずつに分けた区間も認識します。wholeでは区間全体だけを認識します。両方の結果を照合して省かれた発話を補います。分割した音声は重複しません
- 出力をリダイレクトした場合（非 TTY）は、暫定行を出さず確定行だけを出します
- 推論が表示間隔に追いつかないときは、**古い暫定を捨てて最新を優先**します。
  確定結果の保存を優先します。処理が長時間滞って保存できなかった区間があれば、
  終了時に件数を警告します

### 確定済みの行は書き直しません

一度確定してファイルに書いた行は、後から認識し直して修正することはしません。
12 秒の上限で区切られた行も同様です。
翻訳を有効にした場合も、この**英語原文**は変更せず、別の翻訳表示だけを更新します。

<a id="setup-translation"></a>

### 英語会議を日本語へライブ翻訳する (明示選択)

Whisper で英語を認識し、保存された確定原文だけを、共通設定で選んだ Claude または既存のローカル Ollama に渡します。Claude CLI の認証、または Ollama server とモデルは事前に利用者が用意してください。sokki は daemon 起動やモデル取得をしません。Qwen ASR はこの英語翻訳モードでは使えません。既存 YAML が Qwen に切り替わっている場合は、Whisper 用 `--model` も明示してください。

[Whisper](#setup-whisper)と[Claude](#setup-claude)の準備後、Claudeで使う場合:

```sh
./bin/sokki --backend whisper --lang en \
  --model "$HOME/models/ggml-large-v3-turbo.bin" \
  --translate en-ja --llm-provider claude --llm-model ""
```

モデルのパスは準備した実ファイルに合わせます。入力デバイスは既存YAMLまたは `--device` で指定します。
[Ollama](#setup-ollama)で使う場合は、起動済みサーバーへ翻訳用モデルを事前に取得します:

```sh
ollama pull gemma4:e4b
ollama list
./bin/sokki --backend whisper --lang en \
  --model "$HOME/models/ggml-large-v3-turbo.bin" \
  --translate en-ja --llm-provider ollama --llm-model gemma4:e4b \
  --llm-ollama-url http://127.0.0.1:11434
```

両例とも既存設定に従って自動要約も動き、同じLLMを使います。翻訳だけなら `--auto-summary=false` を追加します。

設定ファイルなら `transcription.backend: whisper`、`transcription.language: en` と、`translation.mode: en-ja`、`llm.provider: ollama`、`llm.model: gemma4:e4b` を指定します。優先順位は明示CLI > YAML > 既定値です。既定の翻訳は無効で、無効時には翻訳用 Ollama へ一切アクセスしません。戻す場合は `--translate ""`、または YAML の `translation` 節を削除します。要約と翻訳のprovider/modelは共通です。Ollama指定時は有効な要約も同じモデルを使います。翻訳だけを止めても共通設定は残ります。既存の `--summary-provider/model/ollama-url` と `summary.provider/model/ollama_url` は共通設定の別名として維持します。CLIの新旧同時指定は同じ項目ごとにエラーです。YAMLは `llm.*` が対応する `summary.*` より優先し、`llm.model: ""` は旧モデル値を明示的に消します。`summary.command` は翻訳有効時には使えません。

原文は従来どおり `<会議>.txt` と IPC に保存されます。翻訳on時は日本語を主表示し、`EN:` に確定原文を併記します。別の `<会議>.ja.txt` に現在の訳と状態、`<会議>.translation.jsonl` にunit/versionの更新履歴を残します。確定原文1行で訳を開始し、次の確定行が最初の行の受領から15秒以内なら2行をまとめて訳し直します。2行目がなければ15秒または終了時にそのunitを閉じます。15秒は原文をまとめる期限で、訳が完成する期限ではありません。先行2原文は文脈だけに使い、訳す対象には含めません。

TTYは英語原文と日本語訳を同じ表示位置で更新し、訳の待機中は「翻訳中…」と表示します。各翻訳unitの番号・状態ヘッダーは表示しません。非TTYはunit/version/state付きの追記で改訂を示します。読み込み中・失敗・終了期限超過でも英語原文と未訳理由を残し、録音と原文保存は続きます。モデル準備は録音開始後の背景処理です。通常の翻訳通信は最大60秒、録音終了後の翻訳処理・保存待ちは原文EOFから最大30秒、その後の最終表示・端末復元待ちは最大2秒です。Claude は安全なCLIプリセットでツール・MCP・セッション履歴を使わず、readinessは実行ファイルの存在のみで認証成功を保証しません。Ollama の `keep_alive:5m` は会議中の常時メモリ保持を保証せず、長い無音の後に再ロードする場合があります。終了時に共有 Ollama モデルを強制unloadしません。

英語原文の確定を待ってから翻訳するため、音声認識の待ち時間もあります。画面の「確定」は表示単位を閉じた意味で、正確な翻訳の保証ではありません。合成英文の製品試験では両providerで否定・条件・金額訂正を保持しましたが、実会議全般の精度は未評価です。Ollamaでは不自然な数値表現があり、要約には原文にない補完も観測しています。Claudeは利用枠・課金の対象になり、次の行での訳し直しも追加呼び出しになります。

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

<a id="setup-mcp"></a>

### MCPの追加依存と事前確認

- **Zellij または tmux のセッション内**で、`panecom` が使えること
- AI クライアント（Claude Code / Codex 等）が MCP に対応していること

手動CLIの録音にはこの節の依存は不要です。MCPを使う場合のみ、先に導入します。

```sh
brew install zellij            # または brew install tmux
```

[panecomの導入手順](https://github.com/konono/panecom#インストール)でバイナリを用意し、
**MCPプロセスからも見えるPATH**へ置きます。ソースからビルドする場合はpanecom側のGo要件にも従ってください
（手元で確認した版はGo 1.27以上、Zellij 0.45以上またはtmux 3.2以上）。
Zellij/tmuxを起動した中のAI用paneから、同じ作業ディレクトリで確認します。

```sh
command -v panecom
panecom register assistant     # 現在のAI用paneを登録。既存のroleがあればその運用に従う
panecom whoami
./bin/sokki mcp --check-panecom
```

新しく開く録音paneにもffmpegと、選択したWhisper/Qwen/要約ツールが必要です。
GUIから起動するMCPクライアントはターミナルのPATHを引き継がないことがあるので、クライアントの環境設定も確認します。

**Qwenのbackend・Python・モデル、共通LLMのモデル・URL、翻訳の有効化はYAMLで設定してください。**
`start_or_attach` にはこれら全てを渡す引数がなく、録音paneが読む設定が使われます。
登録先の `command` も、新機能をビルドしたsokkiの絶対パスにします。

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
| `--summary-provider` | `claude` | 共通 `--llm-provider` の互換別名 |
| `--summary-model` | Claude CLI の既定 | 共通 `--llm-model` の互換別名。Ollama では必須 |
| `--summary-ollama-url` | `http://127.0.0.1:11434` | 共通 `--llm-ollama-url` の互換別名 |

更新のきっかけは「前回から **90 秒**以上経っていて、確定行が **8 行**以上たまった」とき。
未処理の発言があれば遅くとも **180 秒**で更新します。ただし最初の要約は、
予告や挨拶だけで作らないよう確定行を最低 **3 行**待ちます。
**発言が無い間は要約生成を呼びません。** Ollama を明示選択した場合のみ、
モデル準備と会議中の保持更新を背景で行います。
AI は発言ごとに項目を増やすのではなく、同じ話題や繰り返しを統合して
会議ボード全体を書き直します。

設定ファイルでも指定できます。

```yaml
summary:
  enabled: true
  provider: claude            # 既定。ollama は明示指定
  model: ""                   # Claude では省略時に CLI の既定
  # ollama_url: http://127.0.0.1:11434
  command: []                 # ollama と併用不可
  min_new_lines: 8
  min_interval: 90s
  max_interval: 180s
  max_chars: 2000
```

ローカル要約を試す場合は、Ollama server とモデルを別途用意してから明示します。
録音開始はモデルのロードを待ちません。準備中は未反映の発言を保持し、
`get_status` の `summary.readiness` が `loading` / `ready` / `failed` に変わります。
`summary.state` は要約処理そのものの状態です。

```sh
./bin/sokki --summary-provider ollama --summary-model 'qwen3.5:9b' \
  --summary-ollama-url http://127.0.0.1:11434
```

YAML では共通設定の `llm.provider: ollama`、`llm.model: qwen3.5:9b`、
`llm.ollama_url: http://127.0.0.1:11434` を指定します。旧 `summary.*` 名も使えますが、対応する `llm.*` があればそちらが優先されます。
設定の優先順位は明示した CLI フラグ、YAML、既定値の順です。
戻す場合は `--summary-provider claude --summary-model ""`、または YAML の
`llm.provider: claude` と `llm.model: ""` を指定します。YAML に Ollama のモデル名が
残ると Claude CLI にも渡るため、モデル名も一緒に戻します。
Ollama 選択時はモデル名が必須で、`summary.command` との併用はエラーです。
要約・翻訳を両方止めた `--auto-summary=false --translate ""` のときは Ollama にアクセスしません。

Ollama へは `think:false`、JSON schema、temperature 0、seed 42、context 16384、
出力上限 4096 token を指定します。要約 HTTP は最大 60 秒、背景準備は最大 120 秒です。
`keep_alive: 5m` で会議中は約 4 分ごとに保持を更新しますが、server 側の期限や
他アプリの使用状況によって再ロードが生じ得るため、常時 ready や一定の応答時間は保証しません。
終了時はこの会議の更新通信だけを止め、共有 Ollama のモデルを強制 unload しません。
その後は server の TTL で解放されます。準備失敗・応答失敗時も録音は続き、
発言は未反映のまま残り、Claude への自動切替は行いません。
準備中に録音を止めた場合は保存後に準備をキャンセルし、最終要約を再ロードせず
`summary.final_state: failed` と理由を表示します。

> プロバイダーを起動できない場合（コマンドが無い、未対応の provider など）は、
> **自動要約だけを無効にして録音を続けます**。pane の見出しと `start_or_attach`
> の応答、`get_status` の `auto_summary` は実際の状態を返します。

> 要約のため、**確定した会議テキストが AI プロバイダーへ送られます**。
> 起動時の表示と `get_status` で有効かどうかを確認できます。
> 要約用の子プロセスにはツール実行・MCP・会話履歴を渡していません。
> `codex` はツールを無効にした実行形を確認できていないため、プリセットとしては選べません。
> 任意のコマンドを使う場合は設定ファイルの `summary.command` に配列で指定します。

要約生成に失敗したときは反映位置を進めず、間隔を空けて再試行します。
Ollama の背景準備または保持更新が失敗した場合は、自動再ロードしません。
文字起こしは影響を受けません。状態は `get_status` の `summary` で確認できます。

録音を止めると、確定行の保存を終えてから**最終要約**を 1 回行います。
`stop_session` は保存完了時点で応答し、最終要約の結果は `get_status` の
`summary.final_state` に出ます。

### 保存されるファイル

文字起こしに加え、要約が反映されると次の関連ファイルが作られます。

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
  → 選択した backend (既定: whisper-server、明示指定: Qwen worker) で認識
  → 選択した確定処理 (既定: 全体+分割/C7のhybrid、wholeは全体1回)
  → 重複・無音由来の定型句を除去 → ターミナル表示 + ファイル追記
```

直前の区間と時間が近く、内容も重なる場合は重複を抑制します。
無音区間で whisper が出しやすい定型句（「ご視聴ありがとうございました」等）も除外します。

## エラーが出たら

| メッセージ | 対処 |
|---|---|
| `command not found` / 新しいフラグがない | `./bin/sokki --help` と `command -v ffmpeg` を確認。追加ツールは選択した構成だけ個別に `command -v whisper-server` / `command -v claude` / `command -v ollama` / `command -v panecom`。Qwenは指定venvのPythonを確認。新機能を含むソースで `make build` する |
| Qwen Python / `mlx_audio` が見つからない | `requirements-qwen.txt` を入れたvenvのPythonを `--qwen-python` に絶対パスで指定。別Pythonのpipへ入れていないか確認 |
| Qwenモデル・`config.json`・`model.safetensors` が見つからない | 指定revisionのディレクトリ全体を取得。Ollama用モデルやWhisper `.bin` は代用不可 |
| Qwenで `model` / `language` / `prompt` エラー | `--model "" --lang ja` を指定し、`--prompt` と `--server-url` を外す |
| Claude認証・コマンド起動エラー | sokkiと同じユーザー・PATHで `claude` に認証し、[生成確認](#setup-claude)を行う。文字起こしのみなら `--auto-summary=false` |
| Ollama接続拒否 / モデル404 | サーバーの起動、URL、`ollama list` を順に確認。**同じサーバー**へ対象モデルをpullしておく |
| Ollama URLエラー | `http://127.0.0.1:11434` のように数値loopback IPを使用。`localhost` は不可 |
| Ollama応答理由・JSON・上限エラー | 本体バージョン、モデルのschema/think対応を確認。HTTP200だけでは正常完了と扱わない。前の要約と未反映行は保持 |
| 既存 `--server-url` 接続なのにWhisperモデル不在エラー | 現行の起動検証はローカルモデルの指定も必要。`--model` に存在するGGMLモデルを指定 |
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
| `whisper-server が起動しませんでした` | `brew install whisper.cpp`、モデルが ggml 形式か確認 |
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
