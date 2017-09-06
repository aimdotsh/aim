aim.sh 自动安装 MySQL 5.6\7
========================

aim.sh 支持 CentOS 6\7 系列的MySQL 5.6\7.x 二进制包自动安装，并且支持自动配置Slave。

用途
===========

* 支持 MySQL 自动安装
* 支持自动配置 MySQL Slave


配置说明
=========

### etc/config 参数说明:

```
slave=0
masterip=192.168.56.209
masterport=5718
mastersocket=/data/mysql_data/data_5718/mysql.sock
slaveip=192.168.56.209
ssl_user=root
ssl_passwd='password'
PRE_BASEDIR=/data/mysql
PRE_LOGDIR=/log/mysql_log
PRE_DATADIR=/data/mysql_data
MySQL_Pass=aim.sh
BASEDIR=$PRE_BASEDIR/mysql${verdir}
DATADIR=${PRE_DATADIR}/data_${PORT}
MYSQL_DATADIR=$DATADIR
MYSQL_HOME=$BASEDIR
TMPDIR=${PRE_DATADIR}/tmp_${PORT}
LOGDIR=${PRE_LOGDIR}/log_${PORT}
socket=$DATADIR/mysql.sock

```

```txt
slave=0 #是否为Slave库，0 为否， 1 为是
masterip=192.168.56.09 #MySQL主库 IP
masterport=5718 #手动指定 MySQL主库 的端口号，仅slave=1有效
mastersocket=/data/mysql_data/data_5718/mysql.sock #手动指定 MySQL主库 的 sock 文件，仅slave=1有效
slaveip=192.168.56.209 #MySQL Slave 库 IP，仅slave=1有效
ssl_user=root #为了方便配置主从服务器，配置Slave和Master服务器之间免登录的 OS 用户名，通常为root，仅slave=1有效
ssl_passwd='password' # ssl_user 对应的 OS 密码，仅slave=1有效
PRE_BASEDIR=/data/mysql #MySQL安装的目录
PRE_LOGDIR=/log/mysql_log #MySQL日志目录
PRE_DATADIR=/data/mysql_data # #MySQL数据目录

BASEDIR=$PRE_BASEDIR/mysql${verdir} #MySQL安装的目录带版本号，eg mysql5.6/5.7
DATADIR=${PRE_DATADIR}/data_${PORT} #MySQL数据目录带端口号
MYSQL_DATADIR=$DATADIR
MYSQL_HOME=$BASEDIR
TMPDIR=${PRE_DATADIR}/tmp_${PORT} #MySQL tmp 目录带端口号
LOGDIR=${PRE_LOGDIR}/log_${PORT} #MySQL 日志目录带端口号
```
##使用说明
```
./aim.sh -v 版本 -p 端口号
eg
./aim.sh -v 5.7.18 -p 5718

 ```
使用说明：
===
## aim.sh 软件包  https://github.com/aimdotsh/aim/archive/master.zip
搭建主库
===

```
cd /root/
wget -O aim-master.zip https://github.com/aimdotsh/aim/archive/master.zip
unzip aim-master.zip
cd aim-master
#安装 MySQL 主库（Master）：
chmod +x *.sh
#修改 etc/config 配置文件中的 slave=0，修改masterip为服务器的 IP 地址，以此 IP 地址确定 service_id
./aim.sh -v 5.7.18 -p 5718

```
##搭建从库
```
#安装 MySQL 从库（Slave）：
#例如软件包复制到 MySQL服务器的 /root/
unzip aim-master.zip
cd aim-master
#修改 etc/config 配置文件中的 slave=1,修改 masterip 为服务器的 IP 地址,修改 slaveip 为 Slave 库的 IP 地址。此两台机器需要配置 ssl 免登录，确保可以互相连接。
vi aim.sh
slave=1 #设置slave=1
masterip=192.168.56.09 #设置MySQL主库 IP
masterport=5718 #手动指定 MySQL主库 的端口号，仅slave=1有效
mastersocket=/data/mysql_data/data_5718/mysql.sock #手动指定 MySQL主库 的 sock 文件，仅slave=1有效
slaveip=192.168.56.209 #MySQL Slave 库 IP，仅slave=1有效
ssl_user=root #为了方便配置主从服务器，配置Slave和Master服务器之间免登录的 OS 用户名，通常为root，仅slave=1有效
ssl_passwd='password' # ssl_user 对应的 OS 密码，仅slave=1有效
#安装Slave
./aim.sh -v 5.7.18 -p 5718  #建议主从在不同主机上，端口相同。

```
##启动关闭数据库

安装完成之后，MySQL 数据库默认是启动的,会在${BASEDIR} 目录下面生成启动和关闭脚本

关闭MySQL
```
${BASEDIR}/stop_${PORT}.sh
```
启动MySQL
```
${BASEDIR}/start_${PORT}.sh
```
删除aim.sh搭建的数据库
===
```
./unaim.sh  -v 5.7.18 -p 5718
```
此操作会删除配置文件中指定的数据库文件目录请谨慎。 


存在的问题：
===

在搭建Slave的时候会配置Slave主机到Master主机上面的免登录进行数据库备份。部分主机在配置免登录的时候可能会失败，有的主机会提示输入密码，设置的等待超时时间为60s，如果在60s内手动输入密码即可以解决，但是如果超时了，会导致配置Slave失败。解决方案，执行./unaim.sh 删除安装的数据，重新运行./aim.sh在等待输入密码的时候手动输入密码，或者手动配置免登录,如下：
手动配置免登录
```
./ssh-copy-id Master库的ip地址 #根据提示输入密码，完成免登录配置,如：
```
```
./ssh-copy-id 188.188.188.188
```

完成之后继续运行aim.sh即可。
