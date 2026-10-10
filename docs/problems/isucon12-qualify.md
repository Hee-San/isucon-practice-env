### 3. 問題を読む

- [当日マニュアル](https://gist.github.com/mackee/4320c18919c8f6f1867849378a17e651)
- [アプリケーションマニュアル(ISUPORTS)](https://gist.github.com/mackee/460eeb8040389ed5bdeaf2c48327707c)
- [出題動画(YouTube)](https://www.youtube.com/watch?v=75YnJ_3289g)
- [問題の解説と講評](https://isucon.net/archives/56850281.html)(ネタバレあり)

使っている AMI([matsuu/aws-isucon](https://github.com/matsuu/aws-isucon/tree/main/isucon12-qualify))は、本番と次の点が違います。

- ドメインは、マニュアルの `*.t.isucon.dev` ではなく `*.t.isucon.local` です(`.dev` は正規の証明書が無いとブラウザで開けないため)
- TLS 証明書は自己署名です

### 4. Web ページを開く

マニュアルの「ポータルのサーバーリストの IP」は、手元で `ssh -G isu1 | awk '/^hostname /{print $2}'` を実行すると分かります。
下のブロックを手元のターミナルに貼ると、`/etc/hosts` にその IP と、マニュアルに出てくる3つのホスト名を書きます(sudo のパスワードを聞かれます。前にこの手順で足した行は消えます)。

```bash
ip=$(ssh -G isu1 | awk '/^hostname /{print $2}')
h=$(grep -v 'isucon-practice$' /etc/hosts) && printf '%s\n' "$h" "$ip admin.t.isucon.local isucon.t.isucon.local kayac.t.isucon.local # isucon-practice" | sudo tee /etc/hosts > /dev/null
```

https://admin.t.isucon.local/ や https://isucon.t.isucon.local/ を開きます。証明書の警告は越えてください。
マニュアルの `.t.isucon.dev` は `.t.isucon.local` に読み替えます。

### 5. ベンチを回す(未検証)

本番でポータルの「Job Enqueue Form」から頼んでいた負荷走行は、bench 機で直接実行し、結果はその出力で見ます。
打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次で出たサービスを `sudo systemctl disable --now <サービス名>` で止めます。

```bash
systemctl list-units --type=service --state=running | grep -E 'isu|nginx|mysql'
```

ベンチは bench 機で次のように実行します。初回は準備に時間がかかることがあるので、失敗したら少し待ってやり直してください。

```bash
cd ~/bench && ./bench -target-addr 192.168.0.11:443
```
