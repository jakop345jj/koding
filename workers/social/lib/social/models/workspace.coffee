{Module} = require 'jraphical'

JMachine = require './computeproviders/machine'

module.exports = class JWorkspace extends Module

  KodingError  = require '../error'
  {slugify}    = require '../traits/slugifiable'
  {signature, secure, ObjectId} = require 'bongo'

  @share()

  @set

    indexes        :
      originId     : 'sparse'
      slug         : 'sparse'

    schema         :
      name         : String
      slug         : String
      isDefault    :
        type       : Boolean
        default    : no
      channelId    : String
      machineUId   : String
      machineLabel : String
      rootPath     : String
      originId     : ObjectId
      layout       : Object

    sharedMethods  :
      static       :
        create     : signature Object, Function
        deleteById : signature String, Function
        deleteByUid: signature String, Function
        update     : signature String, Object, Function
        fetchByMachines: signature Function
      instance     :
        delete     : signature Function
    sharedEvents   :
      static       : []
      instance     : []


  @create = secure (client, data, callback) ->

    {delegate}    = client.connection
    data.originId = delegate._id

    nickname      = delegate.profile.nickname
    data.slug     = slugify data.name?.toLowerCase()

    {name, slug, machineUId, rootPath, originId, machineLabel} = data

    # we don't support saving layout for now, i will set it to empty object
    # to prevent storing any kind of data in it. -- acetz!
    data.layout = {}

    generateUniqueName { originId, name, machineUId }, (err, res)->

      return callback err  if err?

      { slug, name } = res

      data.name      = name
      data.slug      = slug
      data.rootPath  = "/home/#{nickname}/Workspaces/#{slug}"  unless data.rootPath
      workspace      = new JWorkspace data

      workspace.save (err) ->
        return callback err  if err

        delegate.emit 'NewWorkspaceCreated', workspace
        return callback null, workspace


  generateUniqueName = ({originId, machineUId, name, index}, callback)->

    slug = if index? then "#{name}-#{index}" else name
    slug = slugify slug

    JWorkspace.count { originId, slug, machineUId }, (err, count)->

      return callback err  if err?

      if count is 0

        name = "#{name} #{index}"  if index?
        callback null, { name, slug }

      else

        index ?= 0
        index += 1

        generateUniqueName { originId, machineUId, name, index }, callback


  @fetch = secure (client, query = {}, callback) ->

    query.originId = client.connection.delegate._id

    JWorkspace.some query, limit: 30, callback


  @fetchByMachines$ = secure (client, callback) ->

    client.connection.delegate.fetchUser (err, user) ->
      return callback err  if err

      query = 'users.id': user.getId()

      JMachine.some query, {}, (err, machines) ->
        return callback err  if err

        machineUIds = machines.map (machine) -> machine.uid
        JWorkspace.some machineUId: $in: machineUIds, {}, callback


  @deleteById = secure (client, id, callback)->

    selector   =
      originId : client.connection.delegate._id
      _id      : ObjectId id

    JWorkspace.one selector, (err, ws)->
      return callback err  if err?
      unless ws
        callback new KodingError 'Workspace not found.'
      else
        ws.remove (err)-> callback err


  @deleteByUid = secure (client, uid, callback)->

    selector     =
      originId   : client.connection.delegate._id
      machineUId : uid

    JWorkspace.remove selector, (err)->
      callback err


  delete: secure (client, callback)->

    { delegate } = client.connection

    unless delegate.getId().equals this.originId
      return callback new KodingError 'Access denied'

    @remove callback


  @update: secure (client, id, options, callback)->

    selector   =
      originId : client.connection.delegate._id
      _id      : ObjectId id

    JWorkspace.one selector, (err, ws) ->
      return callback err  if err
      return callback new KodingError 'Workspace not found.'  unless ws

      ws.update options, callback


  @createDefault: (client, machine, callback) ->

    {nickname} = client.connection.delegate.profile

    data =
      name         : 'My Workspace'
      isDefault    : yes
      machineLabel : machine.label
      machineUId   : machine.uid
      rootPath     : "/home/#{nickname}"

    @create client, data, callback
