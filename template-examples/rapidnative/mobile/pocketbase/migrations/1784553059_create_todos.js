migrate(
  function(app) {
    var collection = new Collection({
      name: "todos",
      type: "base",
    listRule: "",
    viewRule: "",
    createRule: "@request.auth.id != ''",
    updateRule: "@request.auth.id != ''",
    deleteRule: "@request.auth.id != ''",
      fields: [
        { name: "title", type: "text", required: true },
        { name: "completed", type: "bool", required: true },
        { name: "created_at", type: "autodate", onCreate: true },
        { name: "updated_at", type: "autodate", onCreate: true, onUpdate: true },
      ],
    });

    return app.save(collection);
  },
  function(app) {
    var collection = app.findCollectionByNameOrId("todos");
    return app.delete(collection);
  }
);
