-- All keys share the cell's Redis hash tag. Byte amounts remain decimal strings:
-- Lua numbers cannot exactly represent all uint64 bandwidth values.
local function add(a,b)
  local carry,out=0,''
  local i,j=#a,#b
  while i>0 or j>0 or carry>0 do
    local x,y=0,0
    if i>0 then x=tonumber(string.sub(a,i,i)); i=i-1 end
    if j>0 then y=tonumber(string.sub(b,j,j)); j=j-1 end
    local n=x+y+carry
    out=tostring(n%10)..out; carry=math.floor(n/10)
  end
  return out
end
local function greater(a,b)
  if #a~=#b then return #a>#b end
  return a>b
end
local function subtract(a,b)
  if greater(b,a) then return '0' end
  local borrow,out=0,''
  local i,j=#a,#b
  while i>0 do
    local x=tonumber(string.sub(a,i,i))-borrow
    local y=0
    if j>0 then y=tonumber(string.sub(b,j,j)); j=j-1 end
    if x<y then x=x+10; borrow=1 else borrow=0 end
    out=tostring(x-y)..out; i=i-1
  end
  out=string.gsub(out,'^0+','')
  if out=='' then return '0' end
  return out
end
local function bounded(a)
  if greater(a,'18446744073709551615') then return '18446744073709551615' end
  return a
end
local op,id,now=ARGV[1],'r:'..ARGV[2],tonumber(ARGV[3])
local input=cjson.decode(ARGV[4])
local dirty=false
local egress=redis.call('hget',KEYS[2],'__egress') or '0'
local ingress=redis.call('hget',KEYS[2],'__ingress') or '0'
local function remove(key)
  local raw=redis.call('hget',KEYS[2],key)
  if raw then
    local r=cjson.decode(raw)
    if r.direction=='EGRESS' then egress=subtract(egress,r.bytes) else ingress=subtract(ingress,r.bytes) end
  end
  redis.call('hdel',KEYS[2],key); redis.call('zrem',KEYS[1],key); redis.call('zrem',KEYS[5],key); dirty=true
end
if op=='evict' then
  redis.call('del',KEYS[1],KEYS[2],KEYS[5]); egress='0'; ingress='0'; dirty=true
end
for _,expired in ipairs(redis.call('zrangebyscore',KEYS[1],'-inf',now)) do remove(expired) end
local cutoff=tonumber(ARGV[7])
if op=='settle' then
  for _,key in ipairs(redis.call('zrangebyscore',KEYS[5],'-inf','('..tostring(cutoff))) do
    remove(key)
  end
end
local raw=redis.call('hget',KEYS[2],id)
local entry=nil
if raw then entry=cjson.decode(raw) end
local allowed=true
if op=='reserve' or op=='import' then
  if entry then
    allowed=entry.direction==input.direction and entry.stream==input.stream
    if op=='import' and allowed and input.state=='HELD' and entry.state=='PENDING' then
      entry.state='HELD'; entry.activated_ms=input.activated_ms
      entry.expires_ms=input.expires_ms; dirty=true
    end
  else
    local reserved=egress
    if input.direction=='INGRESS' then reserved=ingress end
    allowed=op=='import' or not greater(add(add(reserved,ARGV[6]),input.bytes),ARGV[5])
    if allowed and input.expires_ms>now then
      entry=input; dirty=true
      if entry.direction=='EGRESS' then egress=add(egress,entry.bytes) else ingress=add(ingress,entry.bytes) end
    end
  end
elseif op=='activate' then
  allowed=entry~=nil and (input.stream=='' or input.stream==entry.stream)
  if allowed and entry.state=='PENDING' then
    entry.state='HELD'; entry.activated_ms=now; entry.expires_ms=now+120000; dirty=true
  end
elseif op=='release' or op=='release_pending' then
  if entry and (op=='release' or entry.state=='PENDING') then remove(id); entry=nil end
end
if entry then
  redis.call('hset',KEYS[2],id,cjson.encode(entry))
  redis.call('zadd',KEYS[1],entry.expires_ms,id)
  if entry.state=='HELD' then
    redis.call('zadd',KEYS[5],entry.activated_ms,id)
    redis.call('pexpire',KEYS[5],180000)
  end
end
if dirty then
  redis.call('incr',KEYS[4])
  redis.call('hset',KEYS[2],'__revision',redis.call('get',KEYS[4]),'__dirty','1','__egress',egress,'__ingress',ingress)
end
local sums={egress=bounded(egress),ingress=bounded(ingress),revision=redis.call('hget',KEYS[2],'__revision') or '0'}
local pending=redis.call('hget',KEYS[2],'__dirty')=='1'
local clock=redis.call('time')
local publish_ms=tonumber(clock[1])*1000+math.floor(tonumber(clock[2])/1000)
local last=tonumber(redis.call('hget',KEYS[2],'__published_ms') or '0')
local eventID=''
if pending and (op=='evict' or publish_ms-last>=250) then
  local event=cjson.decode(ARGV[8]); event.payload=sums
  eventID=redis.call('xadd',KEYS[3],'MAXLEN','~',ARGV[9],'*','data',cjson.encode(event))
  redis.call('hset',KEYS[2],'__published_ms',publish_ms,'__dirty','0'); pending=false
end
if op=='evict' then redis.call('del',KEYS[1],KEYS[2],KEYS[5]) end
return cjson.encode({allowed=allowed,sums=sums,entry=entry or cjson.null,pending=pending,event_id=eventID})
