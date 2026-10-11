# private-isu

## 問題を読む

- [当日マニュアル](https://github.com/catatsuy/private-isu/blob/master/manual.md)
- [リポジトリの README](https://github.com/catatsuy/private-isu)

社内 ISUCON 向けの問題なので、出題動画や公式の解説はありません。
[達人が教えるWebパフォーマンスチューニング](https://gihyo.jp/book/2022/978-4-297-12846-3) がこの問題を題材にしています。

## Web ページを開く

作成時の Summary に出るブロックを手元のターミナルに貼ると、開く URL が出ます(80 番を開けてあります)。
アカウント名 `mary`、パスワード `marymary` でログインできます。

<!-- url: http -->

## ベンチを回す

本番でポータルに集まっていたスコアは、ベンチの出力で見ます。
打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次を実行します。

```bash
sudo systemctl disable --now isu-ruby nginx mysql memcached
```

ベンチは bench 機で次のように実行します。

```bash
/home/isucon/private_isu/benchmarker/bin/benchmarker \
  -u /home/isucon/private_isu/benchmarker/userdata \
  -t http://192.168.0.11
```

1分ほどで `{"pass":true,"score":...}` が出ます。初期状態(Ruby 実装)は 1,000 点前後が目安です。
`No such file` なら `ls ~` で実際のディレクトリ名を確かめてください(公式 README には `private_isu.git` という名前も載っています)。
