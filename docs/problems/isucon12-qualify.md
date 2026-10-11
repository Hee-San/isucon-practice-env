# ISUCON12 予選

## 問題を読む

- [当日マニュアル](https://gist.github.com/mackee/4320c18919c8f6f1867849378a17e651)
- [アプリケーションマニュアル(ISUPORTS)](https://gist.github.com/mackee/460eeb8040389ed5bdeaf2c48327707c)
- [出題動画(YouTube)](https://www.youtube.com/watch?v=75YnJ_3289g)
- [問題の解説と講評](https://isucon.net/archives/56850281.html)(ネタバレあり)

使っている AMI([matsuu/aws-isucon](https://github.com/matsuu/aws-isucon/tree/main/isucon12-qualify))は、本番と次の点が違います。

- ドメインは、マニュアルの `*.t.isucon.dev` ではなく `*.t.isucon.local` です(`.dev` は正規の証明書が無いとブラウザで開けないため)
- TLS 証明書は自己署名です

## Web ページを開く

手元の `/etc/hosts` に、isu1 の IP と、マニュアルに出てくる3つのホスト名を書きます。作成時の Summary に出るブロックを、手元のターミナルに貼ってください(sudo のパスワードを聞かれます。前にこの手順で書いた行は消えます)。

<!-- hosts: admin.t.isucon.local isucon.t.isucon.local kayac.t.isucon.local -->

https://admin.t.isucon.local/ や https://isucon.t.isucon.local/ を開きます。証明書の警告は越えてください。
マニュアルの `.t.isucon.dev` は `.t.isucon.local` に読み替えます。

## ベンチを回す(未検証)

打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次で出たサービスを `sudo systemctl disable --now <サービス名>` で止めます。

```bash
systemctl list-units --type=service --state=running | grep -E 'isu|nginx|mysql'
```

ベンチは bench 機で次のように実行します。初回は準備に時間がかかることがあるので、失敗したら少し待ってやり直してください。

```bash
cd ~/bench && ./bench -target-addr 192.168.0.11:443
```
