#ifndef GOSCANNER_DNSSD_H
#define GOSCANNER_DNSSD_H

#include <dns_sd.h>
#include <arpa/inet.h>
#include <stdlib.h>
#include <string.h>
#include <sys/select.h>
#include <errno.h>

typedef struct scan_browse scan_browse;
typedef struct {
 scan_browse *owner;
 DNSServiceRef resolve_query, address_query;
 char name[256], type[32], domain[256], host[256], address[INET_ADDRSTRLEN];
 unsigned char txt[2048];
 uint16_t txt_len, port;
 uint32_t iface;
 int active, ready;
} scan_service;
struct scan_browse { DNSServiceRef connection; scan_service services[64]; int count, error; };

static void scan_address(DNSServiceRef sd, DNSServiceFlags flags, uint32_t iface,
 DNSServiceErrorType error, const char *host, const struct sockaddr *address,
 uint32_t ttl, void *context) {
 scan_service *s=context;
 if(error || !s->active || !address || address->sa_family!=AF_INET || strcasecmp(host,s->host)) return;
 if(!(flags & kDNSServiceFlagsAdd)) {s->ready=0;return;}
 const struct sockaddr_in *a=(const struct sockaddr_in *)address;
 if(inet_ntop(AF_INET,&a->sin_addr,s->address,sizeof(s->address))) s->ready=1;
}
static void scan_resolved(DNSServiceRef sd, DNSServiceFlags flags, uint32_t iface,
 DNSServiceErrorType error, const char *full, const char *host, uint16_t port,
 uint16_t txt_len, const unsigned char *txt, void *context) {
 scan_service *s=context;
 if(error || !s->active || strlen(host)>=sizeof(s->host) || txt_len>sizeof(s->txt)) return;
 int changed=strcasecmp(s->host,host)!=0;
 if(changed && s->address_query) {DNSServiceRefDeallocate(s->address_query);s->address_query=NULL;}
 if(changed) {s->ready=0;s->address[0]=0;}
 strcpy(s->host,host); s->port=ntohs(port); s->txt_len=txt_len;
 if(txt_len) memcpy(s->txt,txt,txt_len);
 if(!s->address_query) {
  s->address_query=s->owner->connection;
  if(DNSServiceGetAddrInfo(&s->address_query,kDNSServiceFlagsShareConnection,iface,kDNSServiceProtocol_IPv4,host,scan_address,s)) s->address_query=NULL;
 }
}
static void scan_found(DNSServiceRef sd, DNSServiceFlags flags, uint32_t iface,
 DNSServiceErrorType error, const char *name, const char *type, const char *domain, void *context) {
 scan_browse *b=context;
 if(error) {b->error=error;return;}
 if(strlen(name)>=256 || strlen(type)>=32 || strlen(domain)>=256) return;
 for(int i=0;i<b->count;i++) {
  scan_service *s=&b->services[i];
  if(s->iface==iface && !strcmp(s->name,name) && !strcmp(s->type,type) && !strcmp(s->domain,domain)) {
   if(!(flags & kDNSServiceFlagsAdd)) {
    s->active=0;s->ready=0;
    if(s->address_query) {DNSServiceRefDeallocate(s->address_query);s->address_query=NULL;}
    if(s->resolve_query) {DNSServiceRefDeallocate(s->resolve_query);s->resolve_query=NULL;}
   } else if(!s->active) {
    s->active=1;s->host[0]=0;
    s->resolve_query=b->connection;
    if(DNSServiceResolve(&s->resolve_query,kDNSServiceFlagsShareConnection,iface,name,type,domain,scan_resolved,s)) s->resolve_query=NULL;
   }
   return;
  }
 }
 if(!(flags & kDNSServiceFlagsAdd)) return;
 if(b->count>=64) {b->error=kDNSServiceErr_NoMemory;return;}
 scan_service *s=&b->services[b->count++];
 s->owner=b; s->iface=iface; s->active=1;
 strcpy(s->name,name); strcpy(s->type,type); strcpy(s->domain,domain);
 s->resolve_query=b->connection;
 if(DNSServiceResolve(&s->resolve_query,kDNSServiceFlagsShareConnection,iface,name,type,domain,scan_resolved,s)) s->resolve_query=NULL;
}
static scan_browse *scan_open(void) {
 scan_browse *b=calloc(1,sizeof(*b)); if(!b) return NULL;
 b->error=DNSServiceCreateConnection(&b->connection);
 if(b->error) return b;
 const char *types[]={"_uscan._tcp","_uscans._tcp"};
 for(int i=0;i<2;i++) {
  DNSServiceRef browse=b->connection;
  b->error=DNSServiceBrowse(&browse,kDNSServiceFlagsShareConnection,0,types[i],"local.",scan_found,b);
  if(b->error) break;
 }
 return b;
}
static int scan_poll(scan_browse *b) {
 if(b->error) return b->error;
 int fd=DNSServiceRefSockFD(b->connection);
 if(fd<0 || fd>=FD_SETSIZE) return kDNSServiceErr_Unknown;
 fd_set set; FD_ZERO(&set); FD_SET(fd,&set);
 struct timeval wait={0,50000};
 int rc=select(fd+1,&set,NULL,NULL,&wait);
 if(rc<0) return errno==EINTR ? 0 : kDNSServiceErr_Unknown;
 if(rc>0) return DNSServiceProcessResult(b->connection);
 return 0;
}
static void scan_close(scan_browse *b) {
 if(b) {if(b->connection) DNSServiceRefDeallocate(b->connection);free(b);}
}

#endif
