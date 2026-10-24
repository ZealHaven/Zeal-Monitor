// Package collect discovers listening ports, their owning processes, working
// directories and the hostnames mapped to them.
//
// Implemented in milestone M1 (ss/ps/proc + nginx/cloudflared/hosts parsers +
// docker inspect).
package collect
