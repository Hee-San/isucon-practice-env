#!/bin/bash
# up の実行ログから暗号化した ssh_config を取り出し、復号して ~/.ssh/config の先頭に足す。
# Summary の貼るブロックと同じことを、ブラウザを開かずにする(AI に任せるときなど)。gh と age が要る。
# 使い方: scripts/fetch-ssh-config.sh <run の番号> [秘密鍵。既定 ~/.ssh/id_ed25519]
set -euo pipefail
run=${1:?run の番号を渡してください(gh run list --workflow practice-env で見られる)}
K=${2:-$HOME/.ssh/id_ed25519}
repo=${REPO:-Hee-San/isucon-practice-env}

# ログの行は「ジョブ<TAB>ステップ<TAB>時刻 本文」。時刻までを落として、暗号文の範囲だけを取る
enc=$(gh run view "$run" --repo "$repo" --log | sed -n 's/^.*Z //; /-----BEGIN AGE ENCRYPTED FILE-----/,/-----END AGE ENCRYPTED FILE-----/p')
if [ -z "$enc" ]; then
  echo "run $run のログに ssh_config.age がありません(up の run か、ログに暗号文を出す前の workflow ではないか確かめてください)" >&2
  exit 1
fi
cfg=$(printf '%s\n' "$enc" | age -d -i "$K")

# 目印(isucon-practice または isucon-practice-<name>)ごとに、前に足した分を消してから先頭に足す
mark=$(printf '%s\n' "$cfg" | sed -n 's/^# >>> \([^ ]*\) >>>.*/\1/p' | head -1)
touch ~/.ssh/config
# perl -i はシンボリックリンクを普通のファイルに置き換えるので、中身を変数に読んでから > で書き戻す
old=$(perl -0pe "s/^# >>> \Q$mark\E >>>.*?^# <<< \Q$mark\E <<<\\n//msg" ~/.ssh/config)
{
  printf '%s\n' "$cfg" | awk -v k="$K" '{print} /^  User isucon$/{print "  IdentityFile " k; print "  IdentitiesOnly yes"}'
  if [ -n "$old" ]; then printf '%s\n' "$old"; fi
} > ~/.ssh/config
echo "追加しました: $(printf '%s\n' "$cfg" | awk '/^Host / && $2 != "*" {printf "%s ", $2}')"
