#!/bin/sh
USER=root
PASSWD=root
HOST=10.8.8.8
if [ ! -f "/root/.ssh/id_rsa.pub" ]; then
  ssh-keygen -t rsa -P '' -f /root/.ssh/id_rsa
fi
chmod +x ./auto_ssh.expect.sh
./auto_ssh.expect.sh  $USER $PASSWD $HOST
