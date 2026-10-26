#!/bin/sh
# site.sh - publishes dist-site (make site) to vaglio.health: a private S3 bucket behind CloudFront.
# Needs AWS credentials allowed to write the bucket and invalidate the distribution.
set -eu
BUCKET=${VAGLIO_SITE_BUCKET:-vaglio-website-893675906148}
DIST=${VAGLIO_SITE_DISTRIBUTION:-ES98PRUJTN48U}
[ -f dist-site/index.html ] || { echo "site.sh: run make site first" >&2; exit 1; }
aws s3 sync dist-site "s3://$BUCKET" --delete --only-show-errors
aws cloudfront create-invalidation --distribution-id "$DIST" --paths '/*' --query Invalidation.Id --output text
