#!/usr/bin/env bash
# 初回設定: GitHub Actions が引き受ける IAM ロール(cloudformation/github-oidc.yaml)を作り、RoleArn を表示する。
# AWS CloudShell(東京リージョン)で次の1行を実行する。
#
#   curl -fsSL https://raw.githubusercontent.com/Hee-San/isucon-practice-env/main/scripts/bootstrap.sh | bash
#
# 何度実行してもよい(2回目以降はスタックの更新になる)。
set -euo pipefail

REGION=ap-northeast-1
STACK=github-actions-isucon
REPO=${REPO:-Hee-San/isucon-practice-env}

# GitHub の OIDC プロバイダはアカウントに1つしか作れない。
# このスタックが前に作ったものなら持ち続け、別に作られたものがあるなら作らない。
if current=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query "Stacks[0].Parameters[?ParameterKey=='CreateOIDCProvider'].ParameterValue" \
  --output text 2>/dev/null); then
  create=$current
elif aws iam list-open-id-connect-providers --query 'OpenIDConnectProviderList[].Arn' --output text |
  grep -q 'token.actions.githubusercontent.com'; then
  create=false
else
  create=true
fi
echo "OIDC プロバイダを作るか: $create"

template=$(mktemp --suffix=.yaml)
curl -fsSL "https://raw.githubusercontent.com/$REPO/main/cloudformation/github-oidc.yaml" -o "$template"

aws cloudformation deploy --region "$REGION" \
  --stack-name "$STACK" \
  --template-file "$template" \
  --capabilities CAPABILITY_IAM \
  --no-fail-on-empty-changeset \
  --parameter-overrides GitHubRepo="$REPO" CreateOIDCProvider="$create"

role_arn=$(aws cloudformation describe-stacks --region "$REGION" --stack-name "$STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='RoleArn'].OutputValue" --output text)

cat <<MSG

完了しました。次の2つを GitHub の Secret に登録してください:
  https://github.com/$REPO/settings/secrets/actions/new

  AWS_ROLE_ARN         $role_arn
  ISUCON_GITHUB_USERS  メンバーの GitHub ユーザー名(スペース区切り)
MSG
