#include <dns_sd.h>
#include <stdint.h>
#include <assert.h>
#include <string.h>

static int next_ref=2, kinds[256], live_resolves, live_addresses;
static DNSServiceRef fake_ref(int kind) {
 assert(next_ref<256);kinds[next_ref]=kind;
 if(kind==1) live_resolves++;else live_addresses++;
 return (DNSServiceRef)(uintptr_t)next_ref++;
}
static DNSServiceErrorType mock_resolve(DNSServiceRef *ref,DNSServiceFlags flags,uint32_t iface,const char *name,const char *type,const char *domain,DNSServiceResolveReply callback,void *context) {
 *ref=fake_ref(1);return 0;
}
static DNSServiceErrorType mock_address(DNSServiceRef *ref,DNSServiceFlags flags,uint32_t iface,DNSServiceProtocol protocol,const char *host,DNSServiceGetAddrInfoReply callback,void *context) {
 *ref=fake_ref(2);return 0;
}
static void mock_deallocate(DNSServiceRef ref) {
 int id=(int)(uintptr_t)ref;assert(id>=2 && id<next_ref && kinds[id]);
 if(kinds[id]==1) live_resolves--;else live_addresses--;
 kinds[id]=0;
}
#define DNSServiceResolve mock_resolve
#define DNSServiceGetAddrInfo mock_address
#define DNSServiceRefDeallocate mock_deallocate
#include "../dnssd_darwin.h"

int main(void) {
 scan_browse b={0};b.connection=(DNSServiceRef)(uintptr_t)1;
 const char *name="Scanner",*type="_uscan._tcp",*domain="local.";
 scan_found(NULL,kDNSServiceFlagsAdd,4,0,name,type,domain,&b);
 assert(b.count==1 && live_resolves==1);
 scan_service *s=&b.services[0];
 struct sockaddr_in address={0};address.sin_family=AF_INET;inet_pton(AF_INET,"192.168.1.9",&address.sin_addr);
 scan_resolved(NULL,0,4,0,"Scanner._uscan._tcp.local.","one.local.",htons(80),0,NULL,s);
 scan_address(NULL,kDNSServiceFlagsAdd,4,0,"one.local.",(const struct sockaddr *)&address,120,s);
 assert(s->ready && live_addresses==1);
 for(int i=0;i<100;i++) scan_resolved(NULL,0,4,0,"Scanner._uscan._tcp.local.","one.local.",htons(8080),0,NULL,s);
 assert(s->ready && live_addresses==1 && s->port==8080);
 scan_resolved(NULL,0,4,0,"Scanner._uscan._tcp.local.","two.local.",htons(443),0,NULL,s);
 assert(!s->ready && !s->address[0] && live_addresses==1);
 scan_address(NULL,kDNSServiceFlagsAdd,4,0,"one.local.",(const struct sockaddr *)&address,120,s);
 assert(!s->ready);
 scan_address(NULL,kDNSServiceFlagsAdd,4,0,"two.local.",(const struct sockaddr *)&address,120,s);
 assert(s->ready);
 scan_found(NULL,0,4,0,name,type,domain,&b);
 assert(!s->active && !s->ready && live_resolves==0 && live_addresses==0);
 scan_found(NULL,kDNSServiceFlagsAdd,4,0,name,type,domain,&b);
 assert(s->active && !s->ready && b.count==1 && live_resolves==1);
 scan_resolved(NULL,0,4,0,"Scanner._uscan._tcp.local.","three.local.",htons(80),0,NULL,s);
 scan_address(NULL,kDNSServiceFlagsAdd,4,0,"three.local.",(const struct sockaddr *)&address,120,s);
 assert(s->ready && live_addresses==1);
 scan_found(NULL,0,4,0,name,type,domain,&b);
 assert(live_resolves==0 && live_addresses==0);
 return 0;
}
