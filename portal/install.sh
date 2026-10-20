#!/usr/bin/env bash
# contest モードの bench 機に、練習用のポータル(portal/portal.py)を入れる。起動時の UserData から root で1回だけ実行する。
#
# 使い方: install.sh <回> <ポータルのトークン> <このリポジトリの ref> <競技時間(時間)>
#
#   - bench 機で動いているアプリを止め、ベンチに CPU を譲る(人が bench 機に入れないため)
#   - その回のベンチのコマンドを /etc/practice-portal/command に書く。{ip} が向き先のサーバのプライベート IP になる
#   - ポータルを systemd で 80 番に立てる
set -euo pipefail

problem=$1
token=$2
ref=$3
hours=$4

servers=192.168.0.11,192.168.0.12,192.168.0.13
# ベンチは isucon ユーザーで、ホームから動かす。開けるファイル数の上限はポータルの unit から引き継ぐ
as_isucon="runuser -u isucon -- bash -lc"
case "$problem" in
  isucon14)
    # 決済モック(12345 番)とぶつからないよう、ベンチ側の決済サーバは 12346 番にする
    cmd="cd /home/isucon && ./bench run --addr {ip}:443 --target https://isuride.xiv.isucon.net --payment-url http://192.168.0.10:12346 --payment-bind-port 12346" ;;
  isucon13)
    # 向き先の台の DNS で名前を引く。サーバリストは本番のポータルと同じく3台とも渡す
    cmd="cd /home/isucon && ./bench run --enable-ssl --target https://pipe.u.isucon.local --nameserver {ip} --webapp 192.168.0.11 --webapp 192.168.0.12 --webapp 192.168.0.13" ;;
  isucon12-qualify)
    cmd="ulimit -n 65536 && cd /home/isucon/bench && ./bench -target-addr {ip}:443 -target-url https://t.isucon.local" ;;
  isucon11-qualify)
    # ベンチ自身も JIA として待ち受けるので、bench 機の JIA モックとぶつからない 4999 番にする
    cmd="cd /home/isucon/bench && ./bench -tls -target={ip}:443 -all-addresses=192.168.0.11,192.168.0.12,192.168.0.13 -jia-service-url http://192.168.0.10:4999" ;;
  private-isu)
    servers=192.168.0.11
    cmd="/home/isucon/private_isu/benchmarker/bin/benchmarker -u /home/isucon/private_isu/benchmarker/userdata -t http://{ip}" ;;
  nrb2026)
    cmd="/home/isucon/bench.sh {ip}" ;;
  *) echo "未知の回: $problem" >&2; exit 1 ;;
esac

# bench 機のアプリを止める(nrb2026 の bench 機はアプリを動かしていない。配布用の http.server は止めない)
if [ "$problem" != nrb2026 ]; then
  for u in $(systemctl list-unit-files --type=service --state=enabled --no-legend --plain | awk '{print $1}' |
             grep -E 'isu|nginx|mysql|pdns|memcached|jiaapi|payment' || true); do
    systemctl disable --now "$u" || true
  done
fi

install -d -m 755 /opt/practice-portal
curl -fsSL --retry 5 -o /opt/practice-portal/portal.py \
  "https://raw.githubusercontent.com/Hee-San/isucon-practice-env/$ref/portal/portal.py"
install -d -m 700 /etc/practice-portal
# コマンドに ' は入れない(bash -lc '...' で囲むため)
printf "%s '%s'\n" "$as_isucon" "$cmd" > /etc/practice-portal/command
printf 'PORTAL_TOKEN=%s\n' "$token" > /etc/practice-portal/env
chmod 600 /etc/practice-portal/env /etc/practice-portal/command

cat > /etc/systemd/system/practice-portal.service <<EOF
[Unit]
Description=practice portal (isuxportal compatible)
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/practice-portal/env
ExecStart=/usr/bin/python3 /opt/practice-portal/portal.py --listen 0.0.0.0:80 --command-file /etc/practice-portal/command --servers $servers --hours $hours
LimitNOFILE=1048576
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now practice-portal
