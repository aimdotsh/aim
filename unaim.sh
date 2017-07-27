
USAGE="
Options:
================
    v: The version of MySQL  either  eg:5.6.31 or 5.6.34 or 5.7.18
    p: The port of MySQL eg: 3306,5678
Examples:
================
Install MySQL 5.7.18 on port 57180
    $0 -v 5.7.18 -p 57180
Install MySQL 5.6.31 on port 3306
    $0 -v 5.6.31 -p 3306
"

if [ $# -lt 4 ]
then
   echo "$USAGE"
   exit
fi

while getopts "v:p:hg" opt; do
  case $opt in
    g)
      SKIPBINLOG=true
	echo "gtid is on"
      ;;
    h)
      echo "$USAGE"
      exit 0
      ;;
    p)
        PORT="${OPTARG}"
      ;;
    v)
      if [ $OPTARG == "5.6.31" ] || [ $OPTARG == "5.6.34" ] || [ $OPTARG == "5.7.18" ] || [ $OPTARG == "5.6.35" ];
      then
        ver=$OPTARG
      else
        echo "Invalid -v option, please run again with either '-v 5.6.31' or '-v 5.6.31',or '-v 5.7.18'"
        exit 1
      fi
      ;;
    \?)
      echo "Invalid option: -$OPTARG" >&2
      echo $"$USAGE"
      exit 1
      ;;
    :)
      echo "Option -$OPTARG requires an argument." >&2
      echo $"$USAGE"
      exit 1
      ;;
  esac
done

. ./etc/config
$BASEDIR/bin/mysqladmin -u root -p$MySQL_Pass shutdown -S ${DATADIR}/mysql.sock 

sleep 20
rm -rf ${DATADIR}
rm -rf ${LOGDIR}
rm -rf ${TMPDIR}
rm -rf ${PRE_DATADIR}/my_${PORT}.cnf ${BASEDIR}/start_${PORT}.sh ${BASEDIR}/stop_${PORT}.sh
cp /etc/security/limits.conf.aimbk /etc/security/limits.conf
cp /etc/profile.aimbk /etc/profile
rm -rf /etc/security/limits.conf.aimbk /etc/profile.aimbk
