set pagination off
set debug-file-directory evidence/linux/integration-feasibility/handler-safety/debug-dir:/usr/lib/debug
set breakpoint pending on
handle SIGURG nostop noprint pass
handle SIG60 nostop noprint pass
handle SIGPIPE nostop noprint pass
handle SIGUSR1 nostop noprint pass
handle SIGUSR2 nostop noprint pass
handle SIGHUP nostop noprint pass
break WTF::RealTimeThreads::registerThread
commands
  silent
  printf "=== registerThread on thread %d\n", $_thread
  bt 16
  continue
end
break WTF::RealTimeThreads::setupSignalHandler
commands
  silent
  printf "=== setupSignalHandler on thread %d\n", $_thread
  bt 8
  continue
end
break WTF::RealTimeThreads::promoteThreadToRealTime
commands
  silent
  printf "=== promoteThreadToRealTime\n"
  bt 4
  continue
end
run
