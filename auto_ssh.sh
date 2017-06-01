#!/bin/sh
USER=root
PASSWD=1qaz@WSX
HOST=10.3.220.14
if [ ! -f "/root/.ssh/id_rsa.pub" ]; then
  ssh-keygen -t rsa -P '' -f /root/.ssh/id_rsa
fi
chmod +x ./auto_ssh.expect.sh
./auto_ssh.expect.sh  $USER $PASSWD $HOST
