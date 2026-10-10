# ISUCON14

## 問題を読む

- [当日マニュアル](https://github.com/isucon/isucon14/blob/main/docs/manual.md)
- [アプリケーションマニュアル(ISURIDE)](https://github.com/isucon/isucon14/blob/main/docs/ISURIDE.md)
- [出題動画(YouTube)](https://www.youtube.com/watch?v=UFlcAUvWvrY)
- [問題の解説と講評](https://isucon.net/archives/58869617.html)(ネタバレあり)

使っている AMI([matsuu/aws-isucon](https://github.com/matsuu/aws-isucon/tree/main/isucon14))は、TLS 証明書が自己署名です。ほかは本番と同じです。

## Web ページを開く

手元の `/etc/hosts` に、isu1 の IP と `isuride.xiv.isucon.net` を書きます。作成時の Summary に出るブロックを、手元のターミナルに貼ってください(sudo のパスワードを聞かれます。前にこの手順で書いた行は消えます)。

<!-- hosts: isuride.xiv.isucon.net -->

https://isuride.xiv.isucon.net/ を開きます。証明書の警告は越えてください。

## ベンチを回す

打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次で出たサービスを `sudo systemctl disable --now <サービス名>` で止めます。
決済モック(12345 番)は止めなくて大丈夫です。

```bash
systemctl list-units --type=service --state=running | grep -E 'isu|nginx|mysql'
```

ベンチは bench 機で次のように実行します。決済モックとぶつからないよう、ベンチ側の決済サーバは 12346 番にします。

```bash
./bench run --addr 192.168.0.11:443 --target https://isuride.xiv.isucon.net \
  --payment-url http://192.168.0.10:12346 --payment-bind-port 12346
```

- 静的ファイルの検査で落ちるときは `--skip-static-sanity-check` を足します
- 複数台構成にしたら、`--addr` を入口の台に変えます
