#!/usr/bin/env bash
# 練習用スタック(タグ managed-by=isucon-practice)の状態を SVG バッジにして、
# isucon-status ブランチへ置く。README はこのブランチの badge.svg を表示する。
#
# 必要な環境変数: AWS の認証情報、GITHUB_TOKEN(contents: write)、GITHUB_REPOSITORY
set -euo pipefail

rows=$(aws cloudformation describe-stacks \
  --query "Stacks[?Tags[?Key=='managed-by' && Value=='isucon-practice']].[StackName,StackStatus]" \
  --output text)

msg=""
color="#4c1" # 緑: 何も動いていない
if [ -z "$rows" ]; then
  msg="なし(課金ゼロ)"
else
  color="#fe7d37" # 橙: 稼働中(課金中)
  while read -r name status; do
    case "$status" in
      CREATE_COMPLETE | UPDATE_COMPLETE) state="稼働中" ;;
      *_IN_PROGRESS) state="処理中" ;;
      *) state="異常($status)"; color="#e05d44" ;; # 赤: 作成失敗などで残っている
    esac
    msg="${msg:+$msg / }$name $state"
  done <<<"$rows"
fi
echo "バッジ: $msg"

out=$(mktemp -d)
python3 - "$msg" "$color" "$out/badge.svg" <<'PY'
import sys
from xml.sax.saxutils import escape

label, (msg, color, path) = "ISUCON練習環境", sys.argv[1:]

def width(text):
    # 全角は約12px、半角は約7px(11px のフォントでの目安)
    return sum(12 if ord(c) > 0x2E7F else 7 for c in text) + 10

lw, mw = width(label), width(msg)
w = lw + mw
svg = f'''<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="20" role="img" aria-label="{escape(label)}: {escape(msg)}">
<title>{escape(label)}: {escape(msg)}</title>
<clipPath id="r"><rect width="{w}" height="20" rx="3" fill="#fff"/></clipPath>
<g clip-path="url(#r)">
<rect width="{lw}" height="20" fill="#555"/>
<rect x="{lw}" width="{mw}" height="20" fill="{color}"/>
</g>
<g fill="#fff" text-anchor="middle" font-family="Verdana,DejaVu Sans,Hiragino Sans,Noto Sans CJK JP,sans-serif" font-size="11">
<text x="{lw / 2}" y="14">{escape(label)}</text>
<text x="{lw + mw / 2}" y="14">{escape(msg)}</text>
</g>
</svg>
'''
open(path, "w").write(svg)
PY

# 履歴は要らないので、毎回1コミットだけの孤立ブランチを強制 push する
cd "$out"
git init -q -b isucon-status
git add badge.svg
git -c user.name="github-actions[bot]" \
    -c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
    commit -qm "status: $msg"
git push -qf "https://x-access-token:${GITHUB_TOKEN}@github.com/${GITHUB_REPOSITORY}.git" HEAD:isucon-status
