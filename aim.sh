#!/bin/sh
#====================================================#
#   Author: Brty.liu <https://liups.com>             #
#   Intro:  https://aim.sh                           #
#           https://github.com/aimdotsh/aim          #
#====================================================#

. ./etc/config
clear
echo "This is installing MySQL-$ver for port $PORT"
path=$(dirname $0)
path=${path/\./$(pwd)}

mem=$(cat /proc/meminfo |grep MemTotal|awk '{print $2}')
cnf_mem=$[$mem*6/10/1000]
#cnf_mem=226

function Getlastip()
{
   localip=${masterip}
   if [ $slave == 1 ];then localip=$slaveip
   fi
   last_ip=$(echo $localip|awk -F"." '{print $3$4}')
}

function CheckSystem()
{
        if [ $(id -u) != '0' ]; then
                echo '[Error] Please use root to install AIM.';
                exit;
        fi;

        egrep -i "Red Hat Enterprise Linux Server|CentOS" /etc/redhat-release && SysName='RHEL';
        if [ "$SysName" == ''  ]; then
                echo '[Error] Your system is not supported for aim.';
                exit;
        fi;

        SysBit='32';
        if [ `getconf WORD_BIT` == '32' ] && [ `getconf LONG_BIT` == '64' ]; then
                SysBit='64';
        fi;

	yum install perl -y
        Cpunum=`cat /proc/cpuinfo |grep 'processor'|wc -l`;
        RamTotal=`free -m | grep 'Mem' | awk '{print $2}'`;
        RamSwap=`free -m | grep 'Swap' | awk '{print $2}'`;
        echo "${SysBit}Bit, ${Cpunum}*CPU, ${RamTotal}MB*RAM, ${RamSwap}MB*Swap";
        echo '================================================================';
}


function Remove_soft()
{
#yum -y remove mysql-server mysql;
netstat -nlpt|grep mysql|grep  ${PORT}
if [ $? -eq 0 ]
	then
	echo "MySQL is runing ,aim will exit "
	exit 1
	else
	rpm -qa |grep mysql- &&yum -y remove mysql-*
fi
}

function Check_mysqluser()
{
id mysql >& /dev/null
	if [ $? -ne 0 ]
then
   /usr/sbin/groupadd -g 3306 mysql
   /usr/sbin/useradd -g mysql -u 3306 -d /home/mysql -m mysql -s /sbin/nologin
   #echo "q1w2E#R$" |passwd --stdin mysql
fi
}

function For_limit_env()
{
cp /etc/security/limits.conf /etc/security/limits.conf.aimbk
cat >> /etc/security/limits.conf <<EOF
# add by aim begin
mysql    soft    core       unlimited
mysql    hard    core       unlimited
mysql    soft    nproc       131072
mysql    hard    nproc       131072
mysql    soft    nofile       65536
mysql    hard    nofile       65536
mysql    soft    memlock       396826317
mysql    hard    memlock       396826317
# add by aim end
EOF

cp /etc/profile  /etc/profile.aimbk

cat >>/etc/profile <<EOF
#add by aim begin
export MYSQL_HOME=$BASEDIR
export PATH=$MYSQL_HOME/bin:\$PATH:/usr/bin:/bin:/usr/bin/X11:/usr/local/bin
export MYSQL_DATADIR=$DATADIR
export MYSQL_LOGDIR=$LOGDIR
export TMPDIR=$TMPDIR
export MYSQL_UNIX_PORT=$MYSQL_DATADIR/mysql.sock
export MYSQL_TCP_PORT=$PORT
export PATH
#add by aim end
EOF
}

function Check_dir()
{
if [ ! -d "/data" -o ! -d "/log"  ]; then
  echo "/data or /log  Path does not exist,pls check!"
  exit
else
  mkdir -p  ${PRE_BASEDIR}
  chown -R mysql:mysql ${PRE_BASEDIR}
  mkdir -p $DATADIR
  mkdir -p $TMPDIR
  chown -R mysql:mysql $DATADIR
  chown -R mysql:mysql $TMPDIR
  mkdir -p $LOGDIR
  mkdir -p $LOGDIR/bin_log
  mkdir -p $LOGDIR/innodb_log
  mkdir -p $LOGDIR/relay_log
  chown -R mysql:mysql $LOGDIR
fi
}


function Mysql_config()
{

cd $path/media/

if [ -s mysql-${ver}-linux-glibc2.5-x86_64.tar.gz ]; then
  echo "mysql-${ver}-linux-glibc2.5-x86_64.tar.gz [found]"
  else
  echo "Waring: mysql-${ver}-linux-glibc2.5-x86_64.tar.gz not found!!!"
  echo "PLS cp mysql-${ver}-linux-glibc2.5-x86_64.tar.gz $path/media/"
  exit
fi

if [  ! -d ${PRE_BASEDIR}/mysql-${ver}-linux-glibc2.5-x86_64 ];
then 
tar zxvf mysql-${ver}-linux-glibc2.5-x86_64.tar.gz -C  ${PRE_BASEDIR}
cd ${PRE_BASEDIR} 
echo 'wr'
ln -s  ./mysql-${ver}-linux-glibc2.5-x86_64 ./mysql$verdir
pwd
chown -R mysql:mysql $BASEDIR
fi

if [ -s /etc/my.cnf ]; then
        mv /etc/my.cnf /etc/my.cnf_aimbk
fi
cd $path
if [ $verif7 -eq 7  ]
then 
cp my57.cnf ${PRE_DATADIR}/my_${PORT}.cnf
else
cp my56.cnf ${PRE_DATADIR}/my_${PORT}.cnf
fi
sed -i "s/466632K/${cnf_mem}M/g" ${PRE_DATADIR}/my_${PORT}.cnf
sed -i "s/339901/${last_ip}/g"  ${PRE_DATADIR}/my_${PORT}.cnf
sed -i "s/3306/${PORT}/g"  ${PRE_DATADIR}/my_${PORT}.cnf
sed -i "s:^basedir=.*:basedir=${BASEDIR}:g" ${PRE_DATADIR}/my_${PORT}.cnf 
sed -i "s:^datadir=.*:datadir=${DATADIR}:g" ${PRE_DATADIR}/my_${PORT}.cnf 
sed -i "s:/data/mysql_data/data:${DATADIR}:g" ${PRE_DATADIR}/my_${PORT}.cnf 
sed -i "s:/log/mysql_log:${LOGDIR}:g"  ${PRE_DATADIR}/my_${PORT}.cnf
sed -i "s:/data/mysql_data/tmp:${TMPDIR}:g" ${PRE_DATADIR}/my_${PORT}.cnf 



if [ $slave -eq 1 ];then
	sed -i "s/#read-only=1/read-only=1/g" ${PRE_DATADIR}/my_${PORT}.cnf 
fi

cd  $BASEDIR

if [ $verif7 -eq 7  ]
  then
    echo "$BASEDIR/bin/mysqld --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf  --initialize-insecure --user=mysql --basedir=${BASEDIR} --datadir=${DATADIR}"
    $BASEDIR/bin/mysqld --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf --initialize-insecure --user=mysql --basedir=${BASEDIR} --datadir=${DATADIR}
else

    echo "scripts/mysql_install_db --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf --user=mysql --basedir=${BASEDIR} --datadir=$DATADIR "
    scripts/mysql_install_db --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf --user=mysql --basedir=${BASEDIR} --datadir=$DATADIR 

fi


if [ $? -ne 0 ];then
        echo " mysql   install fail"
        exit 1
fi

#cd $path
#cp mysqld /etc/init.d/mysqld
#sed -i "s:^basedir=.*:basedir=${BASEDIR}:g" /etc/init.d/mysqld 
#sed -i "s:^datadir=.*:datadir=${DATADIR}:g" /etc/init.d/mysqld
#sed -i "s:/data/mysql_data/data/:${DATADIR}/:g" /etc/init.d/mysqld
#sed -i "s:/log/mysql_log:${LOGDIR}:g" /etc/init.d/mysqld
#sed -i "s:/data/mysql_data/tmp:${TMPDIR}:g" /etc/init.d/mysqld
#chmod +x /etc/init.d/mysqld
#chkconfig --add mysqld
#chkconfig mysqld on
#/etc/init.d/mysqld start

cd  ${BASEDIR}
#rm -rf my.cnf
#echo "cd  ${BASEDIR}" >>${BASEDIR}/start_${PORT}.sh
#echo "./bin/mysqld_safe --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf &" >>${BASEDIR}/start_${PORT}.sh
#echo "sleep 3;ps -ef |grep mysqld |grep -v grep" >>${BASEDIR}/start_${PORT}.sh

cat > ${BASEDIR}/start_${PORT}.sh <<EOF
cd  ${BASEDIR}
MY=\$(ps -ef |grep mysqld |grep -v grep|grep $PORT|wc -l)    
    if [ \$MY -ge "2" ];then
       echo "MySQL port:$PORT is running!"
       exit
    fi

./bin/mysqld_safe --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf &
sleep 3
MY=\$(ps -ef |grep mysqld |grep -v grep|grep $PORT|wc -l)
    if [ \$MY -ge "2" ];then
       ps -ef |grep mysqld |grep -v grep|grep $PORT
       echo "MySQL port:$PORT Started [ok]!"
    else
       echo "MySQL port:$PORT Started [false]"
    fi
EOF

#echo "cd  ${BASEDIR}" >>${BASEDIR}/stop_${PORT}.sh
#echo "./bin/mysqladmin -u root -p$MySQL_Pass shutdown -S ${DATADIR}/mysql.sock" >>${BASEDIR}/stop_${PORT}.sh
#echo "sleep 3;ps -ef |grep mysqld |grep -v grep" >>${BASEDIR}/stop_${PORT}.sh


cat > ${BASEDIR}/stop_${PORT}.sh <<EOF
cd  ${BASEDIR}
MY=\$(ps -ef |grep mysqld |grep -v grep|grep $PORT|wc -l)
    if [ \$MY -eq "0" ];then
       echo "MySQL port:$PORT is not runing!"
    exit
    fi

./bin/mysqladmin -u root -p$MySQL_Pass shutdown -S ${DATADIR}/mysql.sock
sleep 2 
MY=\$(ps -ef |grep mysqld |grep -v grep|grep $PORT|wc -l)
    if [ \$MY -eq "0" ];then
       echo "MySQL port:$PORT Stopped [ok]!"
    else
       echo "MySQL port:$PORT  Stopped [false]!"
       echo "MySQL Info:"
       ps -ef |grep mysqld |grep -v grep|grep $PORT
    fi
EOF


chmod +x ${BASEDIR}/start_${PORT}.sh
chmod +x ${BASEDIR}/stop_${PORT}.sh

./bin/mysqld_safe --defaults-file=${PRE_DATADIR}/my_${PORT}.cnf &
if [ $? -ne 0 ];then
        echo " mysql   start  fail"
        exit 1
fi
}

function Mysql_sec()
{
sleep 50
ps -ef |grep mysqld
#echo "$BASEDIR/bin/mysqladmin -u root password $MySQL_Pass  -S ${DATADIR}/mysql.sock"
$BASEDIR/bin/mysqladmin -u root password $MySQL_Pass  -S ${DATADIR}/mysql.sock
#echo "$BASEDIR/bin/mysqladmin -u root  password $MySQL_Pass  -S ${DATADIR}/mysql.sock"
#$BASEDIR/bin/mysqladmin -u root  password $MySQL_Pass  -S ${DATADIR}/mysql.sock
#$BASEDIR/bin/mysql -uroot -p$MySQL_Pass -S ${DATADIR}/mysql.sock  -e "delete from mysql.user where user='';delete from mysql.user where password='';flush privileges;"
}


function Mysql_slave()
{
cd $path
yum install -y expect

which expect >& /dev/null
if [ $? -ne 0 ]
then
 rpm -ivh ./tool/expect-5.44.1.15-5.el6_4.x86_64.rpm
fi


if [ ! -f "/root/.ssh/id_rsa.pub" ]; then
  ssh-keygen -t rsa -P '' -f /root/.ssh/id_rsa
fi

chmod +x ./auto_ssh.expect.sh
./auto_ssh.expect.sh  $ssl_user $ssl_passwd $masterip


##master create repl user
ssh -l root $masterip "$BASEDIR/bin/mysql -uroot -hlocalhost -p$MySQL_Pass -e \"GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* to 'repl'@'$slaveip' identified by 'password';\" "
ssh -l root $masterip "$BASEDIR/bin/mysql -uroot -hlocalhost -p$MySQL_Pass -e \"GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* to 'repl'@'$masterip' identified by 'password';\" "

##dump master data
ssh -l root $masterip "$BASEDIR/bin/mysqldump -uroot -hlocalhost -p$MySQL_Pass --master-data=2 --single-transaction -A >/root/aim/forslavedump.sql"

scp root@$masterip:/root/aim/forslavedump.sql $path 
$BASEDIR/bin/mysql -uroot -hlocalhost -p$MySQL_Pass <$path/forslavedump.sql

##get master master binlog pos
#/data/mysql/mysql5.6/bin/mysql -urepl -ppassword -h$masterip -e "show master status;" |grep -v File>slave_pos.txt
cat forslavedump.sql |grep 'CHANGE MASTER TO MASTER_LOG_FILE' |head -1> slave_pos.txt

#MASTER_POS=$(cat slave_pos.txt|awk -F " " '{print $2}')
#MASTER_FILE=$(cat slave_pos.txt|awk -F " " '{print $1}')
mastera=$(cat slave_pos.txt|awk -F "--" '{print $2}'|awk -F ";" '{print $1}')

$BASEDIR/bin/mysql -uroot -hlocalhost -p$MySQL_Pass -e " $mastera ,  master_host='$masterip',master_user='repl',master_password='password';" 
$BASEDIR/bin/mysql -uroot -hlocalhost -p$MySQL_Pass -e "start slave;" 
$BASEDIR/bin/mysql -uroot -hlocalhost -p$MySQL_Pass -e "show slave status\G;" 

}


Remove_soft
Getlastip
CheckSystem
#Remove_soft
Check_mysqluser
For_limit_env
Check_dir
Mysql_config
Mysql_sec

if [ $slave == "1" ];then
	Mysql_slave	
else
	exit 0
fi


cat <<EOF
#database info
#data_dir:$DATADIR
#mysql program $BASEDIR
#root password:$MySQL_Pass
#for root remote login
grant all privileges on *.* to 'root'@'%' identified by '$MySQL_Pass' with grant option;
#iptables
-A INPUT -m state --state NEW -m tcp -p tcp --dport 3306 -j ACCEPT
EOF
exit
