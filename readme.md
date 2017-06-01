aim.sh 自动安装 MySQL 5.6/7
========================

aim.sh 支持 CentOS 6/7 系列的MySQL 6/7.x 二进制包自动安装，并且支持自动配置Slave。

用途
===========

* 支持 MySQL 自动安装
* 支持自动配置 MySQL Slave


使用
=========

### etc/config 参数说明:

```
slave=0
masterip=178.178.178.178
slaveip=178.178.178.179
ssl_user=root
ssl_passwd=redhat
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
```

```txt
slave=0 #是否为Slave库，0 为否
masterip #MySQL主库 IP
slaveip #MySQL Slave 库 IP
ssl_passwd=redhat #为了方便配置主从服务器，配置Slave和Master服务器之间免登录的 OS 用户名，通常为root
PRE_BASEDIR=/data/mysql
PRE_LOGDIR=/log/mysql_log
PRE_DATADIR=/data/mysql_data

BASEDIR=$PRE_BASEDIR/mysql${verdir}
DATADIR=${PRE_DATADIR}/data_${PORT}
MYSQL_DATADIR=$DATADIR
MYSQL_HOME=$BASEDIR
TMPDIR=${PRE_DATADIR}/tmp_${PORT}
LOGDIR=${PRE_LOGDIR}/log_${PORT}
```
 ```
第一个参数为 MySQL 版本，第二个参数为 端口号
./aim.sh #不加参数，默认安装 5.7.18,端口为 3306
./aim.sh 5.6.29 #安装5.6.29 端口为 3306
./aim.sh 5.7.18 #安装5.7.18 端口为 3306
如果要配置为其他端口：例如端口为 563107
./aim.sh 5.6.34 56340  #安装MySQL-5.6.34, 端口为 56340
同样支持安装5.6和5.7的任意版本，只要确保 MySQL 5.6/5.7的软件包在media目录下面即可。
软件包名称为 mysql-5.6/7.xx-linux-glibc2.5-x86_64.tar.gz
其中xx为软件包的小版本号
 ```
配置说明：
===
## aim.sh 软件包 https://github.com/aimdotsh/aim.git
搭建主库
===

```
#例如软件包复制到 MySQL 服务器的 /root/
unzip aim-master.zip
cd aim
#安装 MySQL 主库（Master）：
chmod +x *.sh
#修改 etc/config 配置文件中的 slave=0，修改masterip为服务器的 IP 地址，以此 IP 地址确定 service_id
./aim.sh  
#之后自动安装，脚本会检测是否存在/data和/log，如果不存在，安装会退出。
#默认安装的MySQL版本为 MySQL 5.7.18，如果要安装其他版本如 MySQL 5.6.34，请执行：
./aim.sh 5.6.34
```
##搭建从库
```
#安装 MySQL 从库（Slave）：
#例如软件包复制到 MySQL服务器的 /root/
unzip aim-master.zip
cd aim
#修改 etc/config 配置文件中的 slave=1,修改 masterip 为服务器的 IP 地址,修改 slaveip 为 Slave 库的 IP 地址。此两台机器需要配置 ssl 免登录，确保可以互相连接。
vi aim.sh
##slave=1
##masterip=188.188.188.188   #Master库的ip
##slaveip=189.189.189.189    #Slave库的ip
##ssl_user=root              #Master主机的OS 用户，默认root
##ssl_passwd=redhat          #ssl_user 对应的密码
#安装Slave
./aim.sh 
#同样默认安装的是MySQL5.6.31版本，如果安装其它版本请执行：
./aim.sh 5.6.34
```
删除aim.sh搭建的数据库
===
```
./unaim.sh
```
此操作会删除 /data/mysql、/data/mysql\_data 及/log/mysql\_log目录 


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
