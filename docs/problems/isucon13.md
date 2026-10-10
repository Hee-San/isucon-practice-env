# ISUCON13

## 問題を読む

- [当日マニュアル](https://github.com/isucon/isucon13/blob/main/docs/cautionary_note.md)
- [アプリケーションマニュアル(ISUPipe)](https://github.com/isucon/isucon13/blob/main/docs/isupipe.md)
- [出題動画(YouTube)](https://www.youtube.com/watch?v=OOyInZbM85k)
- [問題の解説と講評](https://isucon.net/archives/58001272.html)(ネタバレあり)

使っている AMI([matsuu/aws-isucon](https://github.com/matsuu/aws-isucon/tree/main/isucon13))は、本番と次の点が違います。

- ドメインは、マニュアルの `*.u.isucon.dev` ではなく `*.u.isucon.local` です(`.dev` は正規の証明書が無いとブラウザで開けないため)
- TLS 証明書は自己署名です

## Web ページを開く

手元の `/etc/hosts` に、isu1 の IP と `pipe.u.isucon.local` などを書きます。作成時の Summary に出るブロックを、手元のターミナルに貼ってください(sudo のパスワードを聞かれます。前にこの手順で書いた行は消えます)。
ほかのサブドメインを見たいときは、書かれた行の末尾の `# isucon-practice` の前に足してください。

<!-- hosts: pipe.u.isucon.local test001.u.isucon.local -->

https://pipe.u.isucon.local/ を開きます。証明書の警告は越えてください。

## ベンチを回す(未検証)

打つ前にチームのチャンネルで宣言してください(同時に打つと互いのスコアが壊れます)。

初回だけ、次の2つをやります。

1. bench 機で動いているアプリを止めて、ベンチに CPU を譲ります。`ssh bench` で入って、次で出たサービスを `sudo systemctl disable --now <サービス名>` で止めます

   ```bash
   systemctl list-units --type=service --state=running | grep -E 'isu|nginx|mysql|pdns'
   ```

2. `ssh isu1` で入って、`~/env.sh` の `ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS` を `"192.168.0.11"` に書き換え、アプリ(`systemctl list-units | grep isupipe` で出るもの)を再起動します。
   AMI の初期値は `127.0.0.1` で、そのままだと bench 機が名前を引いても自分自身に向かうためです

ベンチは bench 機で次のように実行します。isu1 の DNS で名前を引き、`--nameserver` と `--webapp` の台を負荷の対象にします(本番でポータルが持っていた「3台の IP」の代わりです)。

```bash
./bench run --enable-ssl --target https://pipe.u.isucon.local \
  --nameserver 192.168.0.11 --webapp 192.168.0.12 --webapp 192.168.0.13
```

通らなければ、まず isu1 の上で `./bench run --enable-ssl`(1台で完結する形)が通るか確かめてください。
