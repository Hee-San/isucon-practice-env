### 3. 問題を読む

- [当日マニュアル](https://github.com/isucon/isucon14/blob/main/docs/manual.md)
- [アプリケーションマニュアル(ISURIDE)](https://github.com/isucon/isucon14/blob/main/docs/ISURIDE.md)
- [出題動画(YouTube)](https://www.youtube.com/watch?v=UFlcAUvWvrY)
- [問題の解説と講評](https://isucon.net/archives/58869617.html)(ネタバレあり)

使っている AMI([matsuu/aws-isucon](https://github.com/matsuu/aws-isucon/tree/main/isucon14))は、TLS 証明書が自己署名です。ほかは本番と同じです。

### 4. Web ページを開く

マニュアルの「ポータルのサーバーリストの IP」は、手元で `ssh -G isu1 | awk '/^hostname /{print $2}'` を実行すると分かります。
下のブロックを手元のターミナルに貼ると、`/etc/hosts` にその IP と `isuride.xiv.isucon.net` を書きます(sudo のパスワードを聞かれます。前にこの手順で足した行は消えます)。

```bash
ip=$(ssh -G isu1 | awk '/^hostname /{print $2}')
h=$(grep -v 'isucon-practice$' /etc/hosts) && printf '%s\n' "$h" "$ip isuride.xiv.isucon.net # isucon-practice" | sudo tee /etc/hosts > /dev/null
```

https://isuride.xiv.isucon.net/ を開きます。証明書の警告は越えてください。

### 5. ベンチを回す

本番でポータルの「Job Enqueue Form」から頼んでいた負荷走行は、bench 機で直接実行し、結果はその出力で見ます。
打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次で出たサービスを `sudo systemctl disable --now <サービス名>` で止めます。
決済モック(12345 番)は止めなくて大丈夫です。

```bash
systemctl list-units --type=service --state=running | grep -E 'isu|nginx|mysql'
```

ベンチは bench 機で次のように実行します。

```bash
./bench run . run --addr 192.168.0.11:443 --target https://isuride.xiv.isucon.net \
  --payment-url http://192.168.0.10:12346 --payment-bind-port 12346
```

- 静的ファイルの検査で落ちるときは `--skip-static-sanity-check` を足します
- 複数台構成にしたら、`--addr` を入口の台に変えます
