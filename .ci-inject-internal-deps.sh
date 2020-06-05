#!/bin/sh

DEP_FILE="Gopkg.toml"

# remove ignored internal deps
sed -i '/ignored = \["github.com\/safing\//d' $DEP_FILE

# portbase
PORTBASE_BRANCH="develop" 
git branch | grep "* master" >/dev/null
if [ $? -eq 0 ]; then
    PORTBASE_BRANCH="master" 
fi
echo "
[[constraint]]
  name = \"github.com/safing/portbase\"
  branch = \"${PORTBASE_BRANCH}\"
" >> $DEP_FILE

# portmaster
PORTMASTER_BRANCH="develop" 
git branch | grep "* master" >/dev/null
if [ $? -eq 0 ]; then
    PORTMASTER_BRANCH="master" 
fi
echo "
[[constraint]]
  name = \"github.com/safing/portmaster\"
  branch = \"${PORTMASTER_BRANCH}\"
" >> $DEP_FILE
