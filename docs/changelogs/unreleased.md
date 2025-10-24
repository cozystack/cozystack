# Changes after v0.37.0

* [lineage] Break webhook out into a separate daemonset. Reduce unnecessary webhook calls by marking handled resources and excluding them from consideration by the webhook's object selector (@lllamnyp in #1515). 
