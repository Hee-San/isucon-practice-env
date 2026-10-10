# ISUCON11 予選

## 問題を読む

- [当日マニュアル](https://github.com/isucon/isucon11-qualify/blob/main/docs/manual.md)
- [アプリケーションマニュアル(ISUCONDITION)](https://github.com/isucon/isucon11-qualify/blob/main/docs/isucondition.md)
- [出題動画(YouTube)](https://www.youtube.com/watch?v=P-iJ01-riTw)
- [問題の解説と講評](https://isucon.net/archives/56044867.html)(ネタバレあり)

使っている AMI([matsuu/aws-isucon](https://github.com/matsuu/aws-isucon/tree/main/isucon11-qualify))は、JIA のモック(5000 番)が常に動いています。

## Web ページを開く

作成時の Summary に出るブロックを手元のターミナルに貼ると、開く URL が出ます。証明書の警告は越えてください(Firefox だと開けないことがあります)。

<!-- url: https -->

ログインには、isu1 の JIA モックへのポート転送が要ります。手元で次を実行したまま、ブラウザでログインします。
macOS で 5000 番が使われているときは、配布元の [README](https://github.com/matsuu/aws-isucon/tree/main/isucon11-qualify) を見てください。

```bash
ssh -L 5000:127.0.0.1:5000 isu1
```

## ベンチを回す(未検証)

打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次で出たサービスを `sudo systemctl disable --now <サービス名>` で止めます。
JIA モック(`jiaapi-mock`)は止めなくて大丈夫です。

```bash
systemctl list-units --type=service --state=running | grep -E 'isu|nginx|mysql'
```

ベンチは bench 機で次のように実行します。ベンチ自身も JIA として `-jia-service-url` のポートで待ち受けるので、bench 機の JIA モックとぶつからない 4999 番にします。

```bash
cd ~/bench && ./bench -tls -target=192.168.0.11:443 \
  -all-addresses=192.168.0.11,192.168.0.12,192.168.0.13 \
  -jia-service-url http://192.168.0.10:4999
```
