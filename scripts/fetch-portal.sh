#!/bin/bash
# contest の環境の up の実行ログから、暗号化したポータルの URL とトークンを取り出して復号し、
# isuloop が読む Cookie(~/.config/isuloop/portal-cookie)を書く。Summary の「ポータル」のブロックと同じことを、
# ブラウザを開かずにする(AI に任せるときなど)。gh と age が要る。
# 使い方: scripts/fetch-portal.sh <run の番号> [秘密鍵。既定 ~/.ssh/id_ed25519]
set -euo pipefail
run=${1:?run の番号を渡してください(gh run list --workflow practice-env で見られる)}
K=${2:-$HOME/.ssh/id_ed25519}
repo=${REPO:-Hee-San/isucon-practice-env}

# ログには ssh_config.age、portal.age の順に暗号文が出る。2つ目を取る
enc=$(gh run view "$run" --repo "$repo" --log | awk '{ sub(/^.*Z /, "") } /-----BEGIN AGE ENCRYPTED FILE-----/ { n++ } n == 2 { print } n == 2 && /-----END AGE ENCRYPTED FILE-----/ { exit }')
if [ -z "$enc" ]; then
  echo "run $run のログに portal.age がありません(contest で作った up の run か確かめてください)" >&2
  exit 1
fi
read -r u t <<< "$(printf '%s\n' "$enc" | age -d -i "$K")"
mkdir -p ~/.config/isuloop
( umask 077 && printf '_isuxportal_session=%s\n' "$t" > ~/.config/isuloop/portal-cookie )
echo "Cookie を ~/.config/isuloop/portal-cookie に書きました"
echo "isuloop.yaml の bench.portal.url: $u"
echo "ブラウザ: $u/session/new/github?token=<トークン>(トークンは Cookie の値)"
