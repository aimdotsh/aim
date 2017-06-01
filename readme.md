#aim.sh 使用说明
```
目前aim.sh支持CentOS 6/7系列的MySQL 6/7.x二进制包自动安装，并且支持自动配置Slave。

```
##配置说明：
## aim软件包 https://github.com/aimdotsh/aim.git
##搭建主库

```
#例如软件包复制到 MySQL服务器的 /root/
unzip aim-last.zip
cd aim
#安装 MySQL 主库（Master）：
chmod +x *.sh
./aim.sh  
#之后自动安装，脚本会检测是否存在/data和/log，如果不存在，安装会退出。
#默认安装的MySQL版本为 MySQL 5.6.31，如果要安装其他版本如 MySQL 5.6.34，请执行：
./aim.sh 34
```
##搭建从库
```
#安装 MySQL 从库（Slave）：
#例如软件包复制到 MySQL服务器的 /root/
unzip aim-last.zip
cd aim
#修改aim.sh中的配置文件
vi aim.sh
##slave=1
##masterip=188.188.188.188   #Master库的ip
##slaveip=189.189.189.189    #Slave库的ip
##ssl_user=root              #Master主机的OS 用户，默认root
##ssl_passwd=redhat          #ssl_user 对应的密码
#安装Slave
./aim.sh 
#同样默认安装的是MySQL5.6.31版本，如果安装其它版本请执行：
./aim.sh 34
```
##删除aim.sh搭建的数据库
```
./unaim.sh
```
---
###此操作会删除 /data/mysql、/data/mysql\_data 及/log/mysql\_log目录 
---

##存在的问题：

在搭建Slave的时候会配置Slave主机到Master主机上面的免登录进行数据库备份。部分主机在配置免登录的时候可能会失败，有的主机会提示输入密码，设置的等待超时时间为60s，如果在60s内手动输入密码即可以解决，但是如果超时了，会导致配置Slave失败。解决方案，执行./unaim.sh 删除安装的数据，重新运行./aim.sh在等待输入密码的时候手动输入密码，或者手动配置免登录,如下：
##手动配置免登录

./ssh-copy-id Master库的ip地址 #根据提示输入密码，完成免登录配置,如：

```
./ssh-copy-id 188.188.188.188

```
完成之后继续运行aim.sh即可。


 
 