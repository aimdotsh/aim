. ./etc/config
$BASEDIR/bin/mysqladmin -u root -p$MySQL_Pass shutdown -S ${DATADIR}/mysql.sock 

sleep 20
rm -rf ${DATADIR}
rm -rf ${LOGDIR}
rm -rf ${TMPDIR}
cp /etc/security/limits.conf.aimbk /etc/security/limits.conf
cp /etc/profile.aimbk /etc/profile
rm -rf /etc/security/limits.conf.aimbk /etc/profile.aimbk
