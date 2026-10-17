#!/usr/bin/env bash
# nrb2026(ISUNARABE 合同演習2026)の練習環境を、素の Ubuntu 24.04 の上に組み立てる。
# 公開された AMI が無いため、cloudformation/practice-env.yaml の UserData が起動時に root で呼ぶ。
#
#   provision.sh <bench|isu1|isu2|isu3> <WaitConditionHandle の URL> <このリポジトリ(owner/name)> <コミット>
#
# bench: 公開されている bench と seed-gen を Rust でビルドし、椅子の画像(1.5GB)を落として
#        seed.sql とフロントエンドを作る。できたものを VPC の中だけに HTTP で配る
# isu*:  MySQL と Go を入れ、Go の初期実装(このリポジトリの nrb2026/webapp/go)を 80 番で動かす。
#        seed.sql とフロントエンドは bench から受け取る
#
# 終わったら WaitCondition に成功を、失敗したらログの末尾を送る。スタックの作成はこれを待つ。
set -Eeuo pipefail

role=$1
signal_url=$2
repo=$3
ref=$4

# 本家の作問リポジトリ。ベンチ・seed・SQL・フロントエンドはここのものをそのまま使う
NRB_REPO=KOBA789/nrb2026
NRB_REF=9b43a13993104d7a70a29577b4733833914917ef
GO_VERSION=1.24.7
NODE_VERSION=22.12.0
PNPM_VERSION=10.29.3 # 本家の frontend/package.json の packageManager と同じ
BENCH_IP=192.168.0.10
ARTIFACT_PORT=8000

LOG=/var/log/nrb2026-provision.log
exec > >(tee -a "$LOG") 2>&1

export HOME=/root
export DEBIAN_FRONTEND=noninteractive
step=start

signal() { # signal <SUCCESS|FAILURE> <理由>
  local body
  body=$(python3 -c 'import json, sys
print(json.dumps({"Status": sys.argv[1], "Reason": sys.argv[2][:3000], "UniqueId": sys.argv[3], "Data": sys.argv[1]}))' \
    "$1" "$2" "$role")
  curl -fsS --retry 5 -X PUT -H 'Content-Type:' --data-binary "$body" "$signal_url" || true
}
on_error() {
  local tail
  tail=$(tail -n 15 "$LOG" | tr -d '\r' | tr '\n' ' ')
  signal FAILURE "$role: $step で失敗しました。ログの末尾: $tail"
}
trap on_error ERR

apt_install() {
  apt-get -o DPkg::Lock::Timeout=600 update -qq
  apt-get -o DPkg::Lock::Timeout=600 install -y -qq "$@" > /dev/null
}

# 本家のリポジトリを固定したコミットで /root/nrb2026 に展開する
fetch_nrb() {
  rm -rf /root/nrb2026 && mkdir -p /root/nrb2026
  curl -fsSL --retry 5 "https://github.com/$NRB_REPO/archive/$NRB_REF.tar.gz" |
    tar xz -C /root/nrb2026 --strip-components=1
}

provision_bench() {
  step="パッケージの導入"
  apt_install build-essential pkg-config libssl-dev unzip jq curl xz-utils python3
  fetch_nrb
  cd /root/nrb2026

  # 椅子の画像(ISUCON9 予選の 20000 枚、1.5GB)は、ビルドと並行して落とす
  step="椅子の画像の取得"
  ./scripts/fetch-bench-fixtures.sh > /root/fixtures.log 2>&1 &
  fixtures_pid=$!

  step="Rust の導入"
  curl -fsSL --retry 5 https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain none
  # shellcheck source=/dev/null
  . /root/.cargo/env

  step="bench と seed-gen のビルド"
  cargo build --release --quiet -p bench -p seed-gen

  step="椅子の画像の取得"
  wait "$fixtures_pid" || { cat /root/fixtures.log; false; }

  # 本番の配布物と同じ件数と乱数の種(scripts/build.sh の既定値)で作る
  step="seed.sql の生成"
  mkdir -p /srv/nrb2026
  ./target/release/seed-gen full --count 1500 --seed 0xC0FFEE \
    --fixtures bench/fixtures/images --out /srv/nrb2026/seed.sql

  step="フロントエンドのビルド"
  mkdir -p /opt/node
  curl -fsSL --retry 5 "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-x64.tar.xz" |
    tar xJ -C /opt/node --strip-components=1
  (
    export PATH=/opt/node/bin:$PATH
    npm install -g --silent "pnpm@$PNPM_VERSION"
    cd frontend
    pnpm install --frozen-lockfile --reporter=silent
    pnpm run build
  )
  tar czf /srv/nrb2026/public.tar.gz -C frontend/dist .

  step="ベンチの配置"
  install -d -m 755 /opt/bench /opt/bench/fixtures
  install -m 755 target/release/bench /opt/bench/bench
  install -m 755 bench.sh /opt/bench/bench.sh
  rm -rf /opt/bench/fixtures/images
  mv bench/fixtures/images /opt/bench/fixtures/images
  install -m 755 -o isucon -g isucon /dev/stdin /home/isucon/bench.sh <<'EOF'
#!/usr/bin/env bash
# nrb2026 のベンチを回す。対象は既定で isu1(192.168.0.11)。
#   ~/bench.sh                 isu1 に向けて回す
#   ~/bench.sh 192.168.0.12    isu2 に向けて回す
# 本番で benchwarmer が渡していた値を、ここで代わりに渡す。
set -euo pipefail
target=${1:-192.168.0.11}
report=$(mktemp)
trap 'rm -f "$report"' EXIT
# 高い並列度でファイルディスクリプタが足りなくならないよう、上限まで上げる
ulimit -n "$(ulimit -Hn)"
BENCHWARMER_target_ip=$target \
WEBHOOK_URL=http://192.168.0.10:9999/webhook \
REPORT_FD=3 \
  /opt/bench/bench.sh 3> "$report"
echo
echo "score: $(cat "$report")"
EOF

  # 作業に使ったソースとビルド結果は消す(ベンチの中身は本番でも配られていない)
  cd /
  rm -rf /root/nrb2026

  # isu1〜3 に seed.sql とフロントエンドを配る。VPC の外からは届かない(セキュリティグループ)。
  # 2時間で止める
  step="seed.sql とフロントエンドの配布"
  touch /srv/nrb2026/ready
  systemd-run --unit=nrb2026-artifacts --property=RuntimeMaxSec=7200 \
    python3 -m http.server "$ARTIFACT_PORT" --bind "$BENCH_IP" --directory /srv/nrb2026
}

provision_isu() {
  step="パッケージの導入"
  apt_install mysql-server curl jq python3

  # 本番の AMI(mitamae の mysql cookbook)と同じ設定にする
  step="MySQL の設定"
  cat > /etc/mysql/mysql.conf.d/99-nrb2026.cnf <<'EOF'
[mysqld]
default_authentication_plugin = mysql_native_password
EOF
  systemctl restart mysql
  mysql -uroot <<'EOF'
CREATE USER IF NOT EXISTS 'isucon'@'localhost' IDENTIFIED WITH mysql_native_password BY 'isucon';
CREATE USER IF NOT EXISTS 'isucon'@'127.0.0.1' IDENTIFIED WITH mysql_native_password BY 'isucon';
CREATE DATABASE IF NOT EXISTS nrb2026 CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
GRANT ALL PRIVILEGES ON nrb2026.* TO 'isucon'@'localhost';
GRANT ALL PRIVILEGES ON nrb2026.* TO 'isucon'@'127.0.0.1';
FLUSH PRIVILEGES;
EOF

  step="Go の導入"
  rm -rf /usr/local/go
  curl -fsSL --retry 5 "https://go.dev/dl/go$GO_VERSION.linux-amd64.tar.gz" | tar xz -C /usr/local
  # shellcheck disable=SC2016 # ログイン時に展開する
  echo 'export PATH=/usr/local/go/bin:$HOME/go/bin:$PATH' > /etc/profile.d/go.sh

  # Go の初期実装はこのリポジトリから、SQL は本家から持ってくる
  step="初期実装の配置"
  local src
  src=$(mktemp -d)
  curl -fsSL --retry 5 "https://github.com/$repo/archive/$ref.tar.gz" | tar xz -C "$src" --strip-components=1
  fetch_nrb
  install -d -o isucon -g isucon /home/isucon/webapp /home/isucon/webapp/sql
  rm -rf /home/isucon/webapp/go
  cp -r "$src/nrb2026/webapp/go" /home/isucon/webapp/go
  cp /root/nrb2026/webapp/sql/schema.sql /root/nrb2026/webapp/sql/seed.base.sql /home/isucon/webapp/sql/
  rm -rf "$src" /root/nrb2026
  chown -R isucon:isucon /home/isucon/webapp

  step="初期実装のビルド"
  sudo -u isucon -H bash -lc 'cd ~/webapp/go && go build -o nrb2026 .'

  # bench の準備(ビルドと画像の取得で 10 分ほど)が終わるのを待つ
  step="bench の準備待ち"
  local i
  for i in $(seq 1 180); do
    curl -fsS -o /dev/null "http://$BENCH_IP:$ARTIFACT_PORT/ready" && break
    [ "$i" = 180 ] && { echo "30分待っても bench の準備が終わりません"; false; }
    sleep 10
  done

  step="seed.sql とフロントエンドの受け取り"
  curl -fsS --retry 5 -o /home/isucon/webapp/sql/seed.sql "http://$BENCH_IP:$ARTIFACT_PORT/seed.sql"
  rm -rf /home/isucon/webapp/public && mkdir -p /home/isucon/webapp/public
  curl -fsS --retry 5 "http://$BENCH_IP:$ARTIFACT_PORT/public.tar.gz" | tar xz -C /home/isucon/webapp/public
  chown -R isucon:isucon /home/isucon/webapp

  # 本番と同じく nginx を置かず、アプリが 80 番で SPA と API を直接配る
  step="アプリの起動"
  cat > /etc/systemd/system/nrb2026-go.service <<'EOF'
[Unit]
Description=nrb2026 webapp (Go, listens on :80)
After=network-online.target mysql.service
Wants=network-online.target

[Service]
Type=simple
User=isucon
Group=isucon
WorkingDirectory=/home/isucon/webapp/go
Environment=DATABASE_URL=mysql://isucon:isucon@127.0.0.1:3306/nrb2026
Environment=PORT=80
Environment=SQL_DIR=/home/isucon/webapp/sql
Environment=STATIC_DIR=/home/isucon/webapp/public
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ExecStart=/home/isucon/webapp/go/nrb2026
Restart=on-failure
RestartSec=2

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable --now nrb2026-go
  for i in $(seq 1 30); do
    curl -fsS -o /dev/null http://127.0.0.1/healthz && break
    [ "$i" = 30 ] && { journalctl -u nrb2026-go --no-pager | tail -n 20; false; }
    sleep 2
  done

  # ブラウザで開いたときに中身があるよう、初期データを入れておく(ベンチも最初に同じことをする)
  step="初期データの投入"
  curl -fsS --max-time 300 -X POST -d '{"notification_webhook_url":""}' http://127.0.0.1/api/initialize > /dev/null
}

case "$role" in
  bench) provision_bench ;;
  isu1 | isu2 | isu3) provision_isu ;;
  *) echo "未知の役割: $role"; false ;;
esac

signal SUCCESS "$role: 準備ができました"
echo "nrb2026: $role の準備ができました"
