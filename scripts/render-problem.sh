#!/usr/bin/env bash
# docs/problems/<回>.md を、作成時の Summary に出す形に書き換えて標準出力に出す。
#   - 先頭の見出し(# 回の名前)は落とし、## の見出しに続き番号を振って ### にする
#   - <!-- hosts: <ドメイン>... --> を、isu1 の IP とそのドメインを /etc/hosts に書くブロックにする
#   - <!-- url: <http か https> --> を、isu1 の IP から開く URL を出すブロックにする
#   - name 付きの環境では、手元で打つ ssh の Host 名を isu1-<name> などにする
#
# 使い方: render-problem.sh <docs/problems/回.md> <最初の番号> [<name>]
#
# ブロックは手元(macOS 標準の zsh を含む)に貼って使う。zsh は対話シェルで # を
# コメントとして扱わないので、ブロックの中にコメントを書かない。
set -euo pipefail

file=$1
n=$2
suffix=${3:+-$3}
isu1="isu1$suffix"

# isu1 の IP を ip に入れる。~/.ssh/config に Host が無いと ssh -G は Host 名を
# そのまま返すので、IP の形でなければ止める
ip_block() { # ip_block <IP が取れたときに実行する行>
  cat <<EOF
\`\`\`bash
ip=\$(ssh -G $isu1 | awk '/^hostname /{print \$2}')
if ! printf '%s' "\$ip" | grep -Eq '^[0-9]+([.][0-9]+){3}\$'; then
  echo "$isu1 の IP が分かりません。先に 1. のブロックを貼ってください"
else
  $1
fi
\`\`\`
EOF
}

# 行末の目印(" # isucon-practice")が付いた行だけを置き換える
hosts_block() { # hosts_block <ドメイン...>
  ip_block "h=\$(grep -v ' # isucon-practice\$' /etc/hosts) && printf '%s\\n' \"\$h\" \"\$ip $1 # isucon-practice\" | sudo tee /etc/hosts > /dev/null && echo \"/etc/hosts に書きました: \$ip $1\""
}

url_block() { # url_block <http か https>
  ip_block "echo \"$1://\$ip/\""
}

# Host 名の置き換えは、ブロックを差し込む前の本文にだけかける
# (差し込むブロックは最初から isu1-<name> を使っている)。
# 本文の「作成時の Summary に出るブロック」は、Summary の中では「下のブロック」になる
perl -pe "s/\b(ssh(?: -G| -L \S+)?) (isu[123]|bench)\b/\$1 \$2$suffix/g; s/作成時の Summary に出るブロック/下のブロック/g" "$file" |
  while IFS= read -r line; do
    case "$line" in
      '# '*) IFS= read -r line || true ;; # 見出しと、その後ろの空行を落とす
      '## '*)
        echo "### $n. ${line#'## '}"
        n=$((n + 1))
        ;;
      '<!-- hosts: '*' -->')
        d=${line#'<!-- hosts: '}
        hosts_block "${d%' -->'}"
        ;;
      '<!-- url: '*' -->')
        s=${line#'<!-- url: '}
        url_block "${s%' -->'}"
        ;;
      *) printf '%s\n' "$line" ;;
    esac
  done
